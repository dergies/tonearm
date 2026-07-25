package player

import (
	"context"
	"log/slog"

	"codeberg.org/dergs/tonearm/internal/settings"
	"github.com/go-gst/go-gst/gst"
	"github.com/qmuntal/stateless"
)

const (
	STATE_UNINITIALIZED state = "UNINITIALIZED"
	STATE_STOPPED       state = "STOPPED"
	STATE_PAUSED        state = "PAUSED"

	TRIGGER_INITIALIZE trigger = "initialize"
	TRIGGER_PAUSE      trigger = "TRIGGER_PAUSE"
	TRIGGER_RESUME     trigger = "TRIGGER_RESUME"
	TRIGGER_STOP       trigger = "TRIGGER_STOP"
)

var stateMachine = stateless.NewStateMachineWithExternalStorageAndArgs(stateAccessor, stateMutator, stateless.FiringQueued)

func init() {
	// The only allowed move from an unitiaized state is to any initialized state.
	// When the state machine transitions into the initialized state, we always trigger
	// the initialization of the player.
	stateMachine.Configure(STATE_UNINITIALIZED).
		Permit(TRIGGER_INITIALIZE, STATE_STOPPED).
		OnExitWith(TRIGGER_INITIALIZE, func(ctx context.Context, args ...any) error {
			initPlayer()
			return nil
		})

	// This is the default state for the state machine to be in after being initialized
	// or when no playback is happening / scheduled. From there, the state machine has
	// multiple ways to go.
	//
	// There is multiple triggers for playing back a track
	// - Stopped -> playTrackID(trackId) -> Initialized:Preparing:FetchingTrackInfo
	//   - This happens whenever a request for a track happens that we only known the ID of
	// - Stopped -> playTrack(tonearm.Track) -> Initialized:Preparing:FetchingTrack
	//   - This happens whenever a request for a track happens, where we already have all the metadata
	stateMachine.Configure(STATE_STOPPED).
		Permit(TRIGGER_PLAY_TRACK_ID, STATE_PREPARING).
		Permit(TRIGGER_PLAY_TRACK, STATE_PREPARING).
		OnEntry(func(ctx context.Context, args ...any) error {
			if err := playbin.SetState(gst.StateNull); err != nil {
				return err
			}

			PlaybackStateChanged.Notify(func(oldValue *PlaybackState) *PlaybackState {
				newState := *oldValue
				newState.Loading = false
				newState.Status = PlaybackStatusStopped
				return &newState
			})
			return nil
		}).
		OnEntryFrom(gst.MessageEOS, func(ctx context.Context, args ...any) error {
			if !settings.Playback().AllowAutoplay() {
				return nil
			}
			// Since no other songs are left in the queue, retrieve mix to play from API
			logger.Info("starting track radio", "track_id", TrackChanged.CurrentValue().ID())
			return PlayTrackRadio(TrackChanged.CurrentValue().ID(), true)
		})

	stateMachine.Configure(STATE_PAUSED).
		Permit(TRIGGER_PLAY_TRACK_ID, STATE_PREPARING).
		Permit(TRIGGER_PLAY_TRACK, STATE_PREPARING).
		Permit(TRIGGER_RESUME, STATE_PLAYING).
		Permit(TRIGGER_STOP, STATE_STOPPED).
		OnEntry(func(ctx context.Context, args ...any) error {
			if err := playbin.SetState(gst.StatePaused); err != nil {
				return err
			}

			PlaybackStateChanged.Notify(func(oldValue *PlaybackState) *PlaybackState {
				oldValue.Status = PlaybackStatusPaused
				return oldValue
			})
			return nil
		})

	stateMachine.OnTransitioning(func(ctx context.Context, t stateless.Transition) {
		slog.Warn("Transitioning", "source", t.Source, "destination", t.Destination, "trigger", t.Trigger)
	})
	stateMachine.OnTransitioned(func(ctx context.Context, t stateless.Transition) {
		slog.Warn("Transition completed", "source", t.Source, "destination", t.Destination, "trigger", t.Trigger)
	})
	stateMachine.OnUnhandledTrigger(func(ctx context.Context, state stateless.State, trigger stateless.Trigger, unmetGuards []string) error {
		slog.Error("Failed Transition", "state", state, "trigger", trigger, "guards", unmetGuards)
		return nil
	})
	stateMachine.Fire(TRIGGER_INITIALIZE)
}

var storedState stateless.State = STATE_UNINITIALIZED
var storedStateArgs []any

func stateAccessor(context.Context) (stateless.State, []any, error) {
	return storedState, storedStateArgs, nil
}

func stateMutator(ctx context.Context, state stateless.State, args ...any) error {
	storedState = state
	storedStateArgs = args
	return nil
}
