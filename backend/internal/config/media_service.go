package config

// KeyMediaServiceURL is the base URL of the media task service the video Tools and the
// transcription node dispatch to. It is optional: when it is unset none of them is
// registered.
const KeyMediaServiceURL = EnvPrefix + "MEDIA_SERVICE_URL"

// MediaService locates the media task service. The zero value means the service is not
// configured.
type MediaService struct {
	BaseURL string
}

// Enabled reports whether a media service is configured.
func (m MediaService) Enabled() bool {
	return m.BaseURL != ""
}

// mediaService reads the optional service location. A set value must be a base URL that
// carries no credential of its own.
func (l *loader) mediaService() MediaService {
	return MediaService{BaseURL: l.optionalProviderURL(KeyMediaServiceURL)}
}
