package object

import (
	"testing"

	discovery "k8s.io/api/discovery/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func boolp(b bool) *bool { return &b }

// TestEndpointSliceConditions pins which endpoints reach the cache for every
// combination of conditions the EndpointSlice API can report, with and without
// the terminating_endpoints option.
//
// The row that motivates the option is the terminating endpoint that is still
// serving: the API requires Ready to be false there, so filtering on Ready
// alone drops a pod for the whole of its graceful shutdown.
func TestEndpointSliceConditions(t *testing.T) {
	tests := []struct {
		name                     string
		ready, serving, term     *bool
		wantDefault, wantIncTerm bool
	}{
		{"unreported conditions are ready", nil, nil, nil, true, true},
		{"ready and serving", boolp(true), boolp(true), boolp(false), true, true},
		{"not ready, not terminating", boolp(false), boolp(false), boolp(false), false, false},
		{"terminating, still serving", boolp(false), boolp(true), boolp(true), false, true},
		{"terminating, stopped serving", boolp(false), boolp(false), boolp(true), false, false},
		{"ready, serving unreported", boolp(true), nil, nil, true, true},
		{"not ready, serving unreported", boolp(false), nil, nil, false, false},
		// Not a combination the API produces: ready is unreported, which
		// consumers read as ready, while serving is explicitly false. The
		// option filters on serving, so it believes the explicit condition.
		{"serving false beats unreported ready", nil, boolp(false), nil, true, false},
	}

	for _, tc := range tests {
		for _, incTerm := range []bool{false, true} {
			slice := &discovery.EndpointSlice{
				ObjectMeta: meta.ObjectMeta{
					Name:      "svc1-slice1",
					Namespace: "testns",
					Labels:    map[string]string{discovery.LabelServiceName: "svc1"},
				},
				Endpoints: []discovery.Endpoint{{
					Addresses: []string{"172.0.0.1"},
					Conditions: discovery.EndpointConditions{
						Ready:       tc.ready,
						Serving:     tc.serving,
						Terminating: tc.term,
					},
				}},
			}

			obj, err := endpointSliceToEndpoints(slice, EndpointSliceOpts{IncludeTerminating: incTerm})
			if err != nil {
				t.Fatalf("%s (IncludeTerminating=%v): %s", tc.name, incTerm, err)
			}
			ep := obj.(*Endpoints)

			want := tc.wantDefault
			if incTerm {
				want = tc.wantIncTerm
			}
			got := len(ep.Subsets[0].Addresses) == 1
			if got != want {
				t.Errorf("%s (IncludeTerminating=%v): published=%v, want %v", tc.name, incTerm, got, want)
			}
			if got != (len(ep.IndexIP) == 1) {
				t.Errorf("%s (IncludeTerminating=%v): IndexIP disagrees with the address list", tc.name, incTerm)
			}
		}
	}
}

// TestEndpointSliceOptsZones checks that the option that retains topology zones
// is unaffected by the one that retains terminating endpoints, and that zones
// stay off unless asked for.
func TestEndpointSliceOptsZones(t *testing.T) {
	zone := "us-west-2a"
	slice := func() *discovery.EndpointSlice {
		return &discovery.EndpointSlice{
			ObjectMeta: meta.ObjectMeta{
				Name:      "svc1-slice1",
				Namespace: "testns",
				Labels:    map[string]string{discovery.LabelServiceName: "svc1"},
			},
			Endpoints: []discovery.Endpoint{{
				Addresses: []string{"172.0.0.1"},
				Zone:      &zone,
			}},
		}
	}

	for _, tc := range []struct {
		opts      EndpointSliceOpts
		wantZones bool
	}{
		{EndpointSliceOpts{}, false},
		{EndpointSliceOpts{IncludeTerminating: true}, false},
		{EndpointSliceOpts{WithZones: true}, true},
		{EndpointSliceOpts{WithZones: true, IncludeTerminating: true}, true},
	} {
		obj, err := endpointSliceToEndpoints(slice(), tc.opts)
		if err != nil {
			t.Fatalf("%+v: %s", tc.opts, err)
		}
		ep := obj.(*Endpoints)
		if got := ep.Zones != nil; got != tc.wantZones {
			t.Errorf("%+v: Zones populated = %v, want %v", tc.opts, got, tc.wantZones)
		}
		if tc.wantZones && ep.Zones["172.0.0.1"] != zone {
			t.Errorf("%+v: Zones[172.0.0.1] = %q, want %q", tc.opts, ep.Zones["172.0.0.1"], zone)
		}
	}
}

// TestMultiClusterEndpointSliceTransformNeverKeepsZones pins that zone-scoped
// names stay undefined inside multicluster zones however the plugin is
// configured.
func TestMultiClusterEndpointSliceTransformNeverKeepsZones(t *testing.T) {
	zone := "us-west-2a"
	slice := &discovery.EndpointSlice{
		ObjectMeta: meta.ObjectMeta{
			Name:      "svc1-slice1",
			Namespace: "testns",
			Labels:    map[string]string{discovery.LabelServiceName: "svc1"},
		},
		Endpoints: []discovery.Endpoint{{
			Addresses: []string{"172.0.0.1"},
			Zone:      &zone,
			Conditions: discovery.EndpointConditions{
				Ready:       boolp(false),
				Serving:     boolp(true),
				Terminating: boolp(true),
			},
		}},
	}

	obj, err := MultiClusterEndpointSliceTransform(EndpointSliceOpts{WithZones: true, IncludeTerminating: true})(slice)
	if err != nil {
		t.Fatal(err)
	}
	mc := obj.(*MultiClusterEndpoints)
	if mc.Zones != nil {
		t.Errorf("multicluster endpoints retained zones %v, want none", mc.Zones)
	}
	// IncludeTerminating still applies.
	if len(mc.Subsets[0].Addresses) != 1 {
		t.Errorf("terminating endpoint that is still serving was dropped, want it published")
	}
}
