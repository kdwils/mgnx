package gluetun_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/kdwils/mgnx/gluetun"
)

func TestFetchPublicIP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"public_ip": "45.85.144.130",
			"region": "Virginia",
			"country": "United States",
			"city": "Ashburn",
			"location": "39.0437,-77.4875",
			"organization": "AS141039 PacketHub S.A.",
			"postal_code": "20147",
			"timezone": "America/New_York"
		}`))
	}))
	defer srv.Close()

	want := &gluetun.PublicIPResponse{
		PublicIP:     "45.85.144.130",
		Region:       "Virginia",
		Country:      "United States",
		City:         "Ashburn",
		Location:     "39.0437,-77.4875",
		Organization: "AS141039 PacketHub S.A.",
		PostalCode:   "20147",
		Timezone:     "America/New_York",
	}

	c := gluetun.New(srv.URL+"/v1/publicip/ip", srv.Client())
	got, err := c.FetchPublicIP(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestFetchPublicIP_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := gluetun.New(srv.URL+"/v1/publicip/ip", srv.Client())
	_, err := c.FetchPublicIP(context.Background())
	if err == nil {
		t.Fatal("expected error for non-200 status, got nil")
	}
}
