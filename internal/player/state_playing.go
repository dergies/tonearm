package player

import (
	"context"
	"time"

	openapi "codeberg.org/dergs/tonearm/pkg/tidalapi/models/openapi"
	v1 "codeberg.org/dergs/tonearm/pkg/tidalapi/models/v1"
	"codeberg.org/dergs/tonearm/pkg/tonearm"
	"github.com/go-gst/go-gst/gst"
)

type trigger string

type state string

const (
	// This state is active as long as the player is playing a track and
	// sound is coming out of the speakers.
	STATE_PLAYING state = "PLAYING"

	// This state is active when the player has enqueued a gapless track
	// and is waiting for the gapless transition to complete.
	STATE_PLAYING_GAPLESS state = STATE_PLAYING + ":GAPLESS"

	// This state is active when the player has encountered an issue during
	// playback (e.g. token expired, network error) and is attempting to
	// recover by retrying the track.
	STATE_PLAYING_RETRYING state = STATE_PLAYING + ":RETRYING"

	// This trigger is used to signal that the player is ready for us to
	// enqueue the next track playback url.
	TRIGGER_GAPLESS_HINT trigger = "TRIGGER_GAPLESS_HINT"

	// This trigger is used to signal that the gapless transition has failed
	// and the player should transition to the default playback state.
	TRIGGER_GAPLESS_FAILED trigger = "TRIGGER_GAPLESS_FAILED"

	// This trigger is used to signal that the playback token has likely
	// expired and the player should transition to the RETRYING state.
	TRIGGER_TOKEN_EXPIRED trigger = "TRIGGER_TOKEN_EXPIRED"
)

func init() {
	stateMachine.Configure(STATE_PLAYING).
		// The application is permitted to transition to the PREPARING state
		// when a different track should be played by ID or reference.
		Permit(TRIGGER_PLAY_TRACK_ID, STATE_PREPARING).
		Permit(TRIGGER_PLAY_TRACK, STATE_PREPARING).
		// The application is permitted to transition to the PAUSED state
		// when the track should be paused.
		Permit(TRIGGER_PAUSE, STATE_PAUSED).
		// The application is permitted to transition to the STOPPED state
		// when the track should be stopped or the end of the track is reached.
		Permit(TRIGGER_STOP, STATE_STOPPED).
		Permit(gst.MessageEOS, STATE_STOPPED).
		// The application is permitted to transition to the GAPLESS state
		// when the gapless hint is received.
		Permit(TRIGGER_GAPLESS_HINT, STATE_PLAYING_GAPLESS).
		// The application is permitted to transition to the RETRYING state
		// when the playback token has likely expired.
		Permit(TRIGGER_TOKEN_EXPIRED, STATE_PLAYING_RETRYING).
		OnEntry(onPlaying).
		OnExit(onPlayingExit)

	stateMachine.Configure(STATE_PLAYING_GAPLESS).
		// The player must be considered PLAYING even when a gapless
		// transition is in progress.
		SubstateOf(STATE_PLAYING).
		// The application is permitted to transition to the PLAYING state
		// when the gapless transition is complete.
		Permit(gst.MessageStreamStart, STATE_PLAYING).
		// The application is permitted to transition to the PLAYING state
		// when the gapless transition fails.
		Permit(TRIGGER_GAPLESS_FAILED, STATE_PLAYING).
		OnEntry(onPlayingGapless).
		OnExitWith(gst.MessageStreamStart, onPlayingGaplessExit)

	stateMachine.Configure(STATE_PLAYING_RETRYING).
		// Even during a retry, the player should be considered
		// to be in the PLAYING state, as it is still playing the track.
		SubstateOf(STATE_PLAYING).
		// When the stream starts, we should transition back to the PLAYING state.
		Permit(gst.MessageStreamStart, STATE_PLAYING).
		// We specifically want to ignore the gapless trigger when retrying
		// as we will fire that trigger as a byproduct of the retry process.
		Ignore(TRIGGER_GAPLESS_HINT).
		OnEntry(onPlayingRetry).
		OnExit(onPlayingRetryExit)
}

func onPlaying(ctx context.Context, args ...any) error {
	// To ensure playbin is definitely in the right state, we set it to playing
	// This might not be necessary. (But its cheap insurance)
	if err := playbin.SetState(gst.StatePlaying); err != nil {
		return err
	}

	PlaybackStateChanged.Notify(func(oldValue *PlaybackState) *PlaybackState {
		// Instead of polling playbin for the duration, we query it once here.
		// This is more efficient AND it solves an issue during gapless playback
		// where the returned value will be updated too early.
		if ok, duration := playbin.QueryDuration(gst.FormatTime); ok {
			oldValue.Duration = time.Duration(duration)
		}
		oldValue.Loading = false
		oldValue.Status = PlaybackStatusPlaying
		return oldValue
	})

	// The playing state owns the update runner, so whenever we enter it
	// we start the update runner to keep the playback state updated.
	startUpdateRunner()
	return nil
}

func onPlayingExit(ctx context.Context, args ...any) error {
	// When we exit the playing state, we stop the update runner as we
	// own its lifecycle.
	stopUpdateRunner()
	return nil
}

func onPlayingGapless(ctx context.Context, args ...any) error {
	repeatMode := RepeatModeChanged.CurrentValue()
	nextTrack := TrackChanged.CurrentValue()

	// If we are not repeating the current track, fetch the next streamable track from the queue.
	if repeatMode != RepeatModeTrack {
		// Fetch the next track from the queue, keep fetching until
		// we hit the next streamable track.
		nextTrack = getNextTrackFromQueue(true)
		for nextTrack != nil && !nextTrack.IsStreamable() {
			getNextTrackFromQueue(false)
			nextTrack = getNextTrackFromQueue(true)
		}
	}

	// If we can't find a streamable track, we return to
	// the default playback state as there is nothing to do.
	if nextTrack == nil {
		return stateMachine.Fire(TRIGGER_GAPLESS_FAILED)
	}

	trackManifest, err := fetchAndEnqueueTrack(nextTrack.ID())
	if err != nil {
		return err
	}

	// HACK: The state machine doesn't offer a way to store state from the entry handler
	// and pass it to the exit handler, so we use stateMutator to store what we need.
	stateMutator(ctx, STATE_PLAYING_GAPLESS, trackManifest, repeatMode)

	return nil
}

func onPlayingGaplessExit(ctx context.Context, args ...any) error {
	_, enqueueArgs, _ := stateMachine.StateWithArgs(ctx)
	track := TrackChanged.CurrentValue()
	repeatMode := enqueueArgs[1].(RepeatMode)

	// If we're not repeating the track, pop the next track from the queue.
	if repeatMode != RepeatModeTrack {
		track = getNextTrackFromQueue(false)

		// Since the track has changed, notify listeners of the new track.
		TrackChanged.Notify(func(oldState tonearm.Track) tonearm.Track {
			return track
		})

		// Gapless playback always results in a history entry
		history.Push(&HistoryEntry{
			TrackID: track.ID(),
		})
	}

	PlaybackStateChanged.Notify(func(oldValue *PlaybackState) *PlaybackState {
		newState := *oldValue
		newState.Duration = track.Duration()
		newState.Position = 0
		return &newState
	})

	PlaybackQualityChanged.Notify(func(oldValue v1.AudioQuality) v1.AudioQuality {
		return openapi.AudioQualityFromFormats(enqueueArgs[0].(*openapi.TrackManifest).Data.Attributes.Formats)
	})
	return nil
}

func onPlayingRetry(ctx context.Context, args ...any) error {
	// When retrying, we need to prepare playbin for a playback url change.
	// Also stopping playback allows us to get a precise position to restore to later.
	playbin.SetState(gst.StateReady)
	if _, err := fetchAndEnqueueTrack(TrackChanged.CurrentValue().ID()); err != nil {
		return err
	}

	// Fetch the current position so we can restore it after retrying.
	position := PlaybackStateChanged.CurrentValue().Position
	if ok, pos := playbin.QueryPosition(gst.FormatTime); ok {
		position = time.Duration(pos)
	}

	// Now that the updated playback url is enqueued, we can set playbin to playing again.
	playbin.SetState(gst.StatePlaying)

	// HACK: The state machine doesn't offer a way to store state from the entry handler
	// and pass it to the exit handler, so we use stateMutator to store what we need.
	stateMutator(ctx, STATE_PLAYING_RETRYING, position)
	return nil
}

func onPlayingRetryExit(ctx context.Context, args ...any) error {
	_, enqueueArgs, _ := stateMachine.StateWithArgs(ctx)
	seekToPositionPrecise(enqueueArgs[0].(time.Duration))
	return nil
}
