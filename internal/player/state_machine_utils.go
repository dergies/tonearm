package player

import (
	"context"
	"errors"
	"fmt"

	"codeberg.org/dergs/tonearm/internal/settings"
	"codeberg.org/dergs/tonearm/pkg/tidalapi"
	openapi "codeberg.org/dergs/tonearm/pkg/tidalapi/models/openapi"
	"github.com/infinytum/injector"
)

var ErrFetchAndEnqueueTrack = fmt.Errorf("failed to fetch and enqueue track")

func fetchAndEnqueueTrack(trackId string) (*openapi.TrackManifest, error) {
	// TODO: This is unfortunately still not done via the service interface
	tidal, err := injector.Inject[*tidalapi.TidalAPI]()
	if err != nil {
		return nil, errors.Join(ErrFetchAndEnqueueTrack, err)
	}

	trackManifest, err := tidal.OpenAPI.V2.TrackManifests.TrackManifest(
		context.Background(),
		trackId,
		openapi.ManifestTypeMPEGDASH,
		openapi.ManifestFormatsForQuality(settings.Player().GetAudioQuality()),
		openapi.ManifestURISchemeHTTPS,
		openapi.ManifestUsagePlayback,
		false,
	)
	if err != nil {
		return nil, errors.Join(ErrFetchAndEnqueueTrack, err)
	}

	if err := enqueue(trackManifest); err != nil {
		return trackManifest, errors.Join(ErrFetchAndEnqueueTrack, err)
	}

	return trackManifest, nil
}
