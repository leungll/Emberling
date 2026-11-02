package taskgateway

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestImageStore_OverBudget_EvictsOldestAndServes404(t *testing.T) {
	f := newGatewayFixture(t, fixtureOptions{imageStoreBytes: 10})
	store := f.server.images
	for _, id := range []string{"img_a", "img_b", "img_c"} {
		if err := store.put(id, stagedImage{mediaType: "image/png", content: bytes.Repeat([]byte{1}, 4)}); err != nil {
			t.Fatalf("put(%s) error = %v", id, err)
		}
	}
	// 12 bytes do not fit a 10-byte budget: the oldest image went first.
	for id, want := range map[string]int{"img_a": http.StatusNotFound, "img_b": http.StatusOK, "img_c": http.StatusOK} {
		if status, _ := f.get(t, "/v1/images/"+id); status != want {
			t.Fatalf("GET /v1/images/%s = %d, want %d", id, status, want)
		}
	}
	recorder := httptest.NewRecorder()
	f.server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/images/img_c", nil))
	if recorder.Header().Get("Content-Type") != "image/png" || recorder.Body.Len() != 4 {
		t.Fatalf("GET img_c = %q with %d bytes, want image/png with 4 bytes", recorder.Header().Get("Content-Type"), recorder.Body.Len())
	}
	if err := store.put("img_huge", stagedImage{content: bytes.Repeat([]byte{1}, 11)}); !errors.Is(err, errImageOverBudget) {
		t.Fatalf("put over the whole budget error = %v, want errImageOverBudget", err)
	}
}
