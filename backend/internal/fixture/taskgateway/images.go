package taskgateway

import (
	"errors"
	"net/http"
	"strconv"
	"sync"
)

// errImageOverBudget reports an image larger than the whole staging budget.
var errImageOverBudget = errors.New("generated image exceeds the image staging budget")

// stagedImage is one generated image held for GET /v1/images/{imageId}.
type stagedImage struct {
	mediaType string
	content   []byte
}

// imageStore holds generated images in memory, bounded by their total size. Once a new
// image would exceed the budget the oldest images are evicted first; an evicted image,
// like any image after a restart, answers 404.
type imageStore struct {
	budget int64

	mu     sync.Mutex
	images map[string]stagedImage
	order  []string
	total  int64
}

func newImageStore(budget int64) *imageStore {
	return &imageStore{budget: budget, images: make(map[string]stagedImage)}
}

// put stages content under id, evicting the oldest images until it fits.
func (s *imageStore) put(id string, image stagedImage) error {
	size := int64(len(image.content))
	if size > s.budget {
		return errImageOverBudget
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.total+size > s.budget && len(s.order) > 0 {
		oldest := s.order[0]
		s.order = s.order[1:]
		s.total -= int64(len(s.images[oldest].content))
		delete(s.images, oldest)
	}
	s.images[id] = image
	s.order = append(s.order, id)
	s.total += size
	return nil
}

func (s *imageStore) get(id string) (stagedImage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	image, ok := s.images[id]
	return image, ok
}

// handleImage serves one staged image. It needs no credential: the id is unguessable and
// the image is the result the callback already reported.
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	image, ok := s.images.get(r.PathValue("imageId"))
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "unknown imageId"})
		return
	}
	w.Header().Set("Content-Type", image.mediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(image.content)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(image.content)
}
