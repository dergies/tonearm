package player

import (
	"context"
	"reflect"

	openapi "codeberg.org/dergs/tonearm/pkg/tidalapi/models/openapi"
	v1 "codeberg.org/dergs/tonearm/pkg/tidalapi/models/v1"
	"codeberg.org/dergs/tonearm/pkg/tonearm"
	"github.com/go-gst/go-gst/gst"
	"github.com/infinytum/injector"
)

const (
	// This state is active as long as we are before actual sound comes
	// out of the speakers. Playback is not yet started or stopped.
	STATE_PREPARING state = "PREPARING"

	// Used whenever the user or app logic requests to play a
	// different track by ID.
	//
	// Trigger Arguments:
	// - TrackID (String): The ID of the track to be resolved and played
	// - SkipHistory (Bool): Whether the track should not create an entry in the playback history
	TRIGGER_PLAY_TRACK_ID trigger = "TRIGGER_PLAY_TRACK_ID"

	// Used whenever the user or app logic requests to play a
	// different track.
	//
	// Trigger Arguments:
	// - Track (tonearm.Track): A reference to the track to be played
	// - SkipHistory (Bool): Whether the track should not create an entry in the playback history
	TRIGGER_PLAY_TRACK trigger = "TRIGGER_PLAY_TRACK"
)

func init() {
	stateMachine.SetTriggerParameters(TRIGGER_PLAY_TRACK_ID, reflect.TypeFor[string](), reflect.TypeFor[bool]())
	stateMachine.SetTriggerParameters(TRIGGER_PLAY_TRACK, reflect.TypeFor[tonearm.Track](), reflect.TypeFor[bool]())

	stateMachine.Configure(STATE_PREPARING).
		// The application is permitted to abort playback before it has begun
		Permit(TRIGGER_STOP, STATE_STOPPED).
		// When the stream starts, we transition to the playback state
		Permit(gst.MessageStreamStart, STATE_PLAYING).
		// When playing a track by ID, we will re-trigger this state using
		// TRIGGER_PLAY_TRACK but we don't want it to re-fire entry / exit handlers.
		InternalTransition(TRIGGER_PLAY_TRACK, onPlayTrack).
		OnEntry(onPreparation).
		OnEntryFrom(TRIGGER_PLAY_TRACK_ID, onPlayTrackID).
		OnEntryFrom(TRIGGER_PLAY_TRACK, onPlayTrack)
}

// Whenever we are in the preparation state, we set the loading flag to true
// to indicate that the player is preparing to play a track.
func onPreparation(ctx context.Context, args ...any) error {
	PlaybackStateChanged.Notify(func(oldValue *PlaybackState) *PlaybackState {
		newState := *oldValue
		newState.Loading = true
		return &newState
	})
	return nil
}

func onPlayTrackID(ctx context.Context, args ...any) error {
	trackId := args[0].(string)
	skipHistoryEntry := args[1].(bool)

	service, err := injector.Inject[tonearm.Service]()
	if err != nil {
		return err
	}

	track, err := service.GetTrack(trackId)
	if err != nil {
		return err
	}

	return stateMachine.FireCtx(ctx, TRIGGER_PLAY_TRACK, track, skipHistoryEntry)
}

func onPlayTrack(ctx context.Context, args ...any) error {
	track := args[0].(tonearm.Track)
	skipHistoryEntry := args[1].(bool)

	// Ensure playbin is prepared to handle a track change. If something is playing, we want to stop it.
	playbin.SetState(gst.StateReady)
	trackManifest, err := fetchAndEnqueueTrack(track.ID())
	if err != nil {
		return err
	}

	// Since we previously stopped playback, we now have to tell playbin to resume playback.
	// Otherwise, it will remain in the Ready state and not play anything.
	if err := playbin.SetState(gst.StatePlaying); err != nil {
		return err
	}

	// Now that we are reasonably sure that playback is going to be successful,
	// we can notify the rest of the application about the new playback information.
	TrackChanged.Notify(func(oldState tonearm.Track) tonearm.Track {
		return track
	})

	PlaybackStateChanged.Notify(func(oldValue *PlaybackState) *PlaybackState {
		newState := *oldValue
		newState.Duration = track.Duration()
		return &newState
	})

	PlaybackQualityChanged.Notify(func(oldValue v1.AudioQuality) v1.AudioQuality {
		return openapi.AudioQualityFromFormats(trackManifest.Data.Attributes.Formats)
	})

	// Only create a history entry if skipHistoryEntry is false
	// i.e. when navigating backwards in the playback history
	if !skipHistoryEntry {
		history.Push(&HistoryEntry{
			TrackID: track.ID(),
		})
	}

	return nil
}
