package components

import (
	"context"
	"os"

	"codeberg.org/dergs/tonearm/internal/resources"
	"codeberg.org/dergs/tonearm/pkg/schwifty"
	"codeberg.org/dergs/tonearm/pkg/schwifty/callback"
	"codeberg.org/dergs/tonearm/pkg/schwifty/state"
	"codeberg.org/dergs/tonearm/pkg/schwifty/tracking"
	"codeberg.org/dergs/tonearm/pkg/tonearm"
	"codeberg.org/dergs/tonearm/pkg/utils/imgutil"
	"codeberg.org/puregotk/puregotk/v4/glib"
	"codeberg.org/puregotk/puregotk/v4/gobject"
	"codeberg.org/puregotk/puregotk/v4/gtk"
	"github.com/infinytum/injector"
)

var coverNotifyHandler = glib.NewCallback(new(func(mediaPtr uintptr, arg2 uintptr) {
	callback.CallbackHandler[any](gtk.MediaFileNewFromInternalPtr(mediaPtr).Object, "notify::prepared", mediaPtr, arg2)
}))

func RenderCover(cover tonearm.Cover, target *state.State[schwifty.Paintable]) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())

	// If the cover has neither a static nor an animated image, use the missing album resource.
	if !cover.HasStatic() && !cover.HasAnimated() {
		target.SetValue(resources.MissingAlbum())
		cancel()
		return cancel
	}

	go func() {
		if cover.HasStatic() {
			texture, err := injector.MustInject[*imgutil.ImgUtil]().FetchCropped(cover.Static())
			if err != nil {
				cancel()
				return
			}
			if ctx.Err() == nil {
				target.SetValue(texture)
			}
		}

		// If the cover has no animated image, there is nothing left to do
		if !cover.HasAnimated() {
			cancel()
			return
		}

		videoBytes, err := injector.MustInject[*imgutil.ImgUtil]().FetchBytes(cover.Animated())
		if err != nil {
			return
		}

		tmpFile, err := os.CreateTemp("", "tonearm-cover-*.mp4")
		if err != nil {
			return
		}
		defer tmpFile.Close()

		_, err = tmpFile.Write(videoBytes)
		if err != nil {
			return
		}

		schwifty.OnMainThreadOncePure(func() {
			media := gtk.NewMediaFileForFilename(tmpFile.Name())

			callback.RegisterDeletionCallback(media.Ptr, func() {
				if err := os.Remove(tmpFile.Name()); err != nil {
					logger.Error("failed to remove temp track cover file", "error", err)
				} else {
					logger.Debug("temp track cover file deleted", "name", tmpFile.Name())
				}
			})

			gobject.SignalConnect(media.Ptr, "notify::prepared", coverNotifyHandler)
			var handlerId int
			handlerId = callback.HandleCallback(media.Object, "notify::prepared", func(mediaptr uintptr, _ uintptr) {
				media := gtk.MediaFileNewFromInternalPtr(mediaptr)
				tracking.SetFinalizer("MediaFile", media)
				callback.DeleteCallback(media.Object, "notify::prepared", handlerId)
				if ctx.Err() == nil {
					target.SetValue(media)
				}
			})

			media.SetLoop(true)
			media.SetMuted(true)
			media.Play()
		})
	}()

	return cancel
}
