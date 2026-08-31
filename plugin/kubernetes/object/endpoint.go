package object

import (
	"fmt"
	"maps"
	"strings"

	discovery "k8s.io/api/discovery/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Endpoints is a stripped down api.Endpoints with only the items we need for CoreDNS.
type Endpoints struct {
	// Don't add new fields to this struct without talking to the CoreDNS maintainers.
	Version   string
	Name      string
	Namespace string
	Index     string
	IndexIP   []string
	Subsets   []EndpointSubset
	// Zones maps address IPs to their topology.kubernetes.io/zone,
	// lowercased. Nil unless the kubernetes plugin's `zonal` option
	// selected the zone-retaining transform, so default configurations
	// carry one nil pointer per slice and their addresses stay exactly
	// as slim as before.
	Zones map[string]string

	*Empty
}

// EndpointSubset is a group of addresses with a common set of ports. The
// expanded set of endpoints is the Cartesian product of Addresses x Ports.
type EndpointSubset struct {
	Addresses []EndpointAddress
	Ports     []EndpointPort
}

// EndpointAddress is a tuple that describes single IP address.
type EndpointAddress struct {
	IP            string
	Hostname      string
	NodeName      string
	TargetRefName string
}

// EndpointPort is a tuple that describes a single port.
type EndpointPort struct {
	Port     int32
	Name     string
	Protocol string
}

// EndpointsKey returns a string using for the index.
func EndpointsKey(name, namespace string) string { return name + "." + namespace }

// EndpointSliceOpts selects what the EndpointSlice transforms keep. The zero
// value is what the plugin has always done: topology zones are dropped, and an
// endpoint is published only while the cluster reports it ready.
type EndpointSliceOpts struct {
	// WithZones retains each endpoint's topology zone. Set by the kubernetes
	// plugin's zonal option, so a default configuration's cache stays exactly
	// as slim as before.
	WithZones bool
	// IncludeTerminating publishes an endpoint that is still serving while it
	// terminates. Set by the kubernetes plugin's terminating_endpoints option.
	IncludeTerminating bool
}

// EndpointSliceTransform returns the ToFunc that converts a
// *discovery.EndpointSlice to a *Endpoints under opts.
func EndpointSliceTransform(opts EndpointSliceOpts) ToFunc {
	return func(obj meta.Object) (meta.Object, error) {
		return endpointSliceToEndpoints(obj, opts)
	}
}

func endpointSliceToEndpoints(obj meta.Object, opts EndpointSliceOpts) (meta.Object, error) {
	ends, ok := obj.(*discovery.EndpointSlice)
	if !ok {
		return nil, fmt.Errorf("unexpected object %v", obj)
	}
	e := &Endpoints{
		Version:   ends.GetResourceVersion(),
		Name:      ends.GetName(),
		Namespace: ends.GetNamespace(),
		Index:     EndpointsKey(ends.Labels[discovery.LabelServiceName], ends.GetNamespace()),
		Subsets:   make([]EndpointSubset, 1),
	}

	if len(ends.Ports) == 0 {
		// Add sentinel if there are no ports.
		e.Subsets[0].Ports = []EndpointPort{{Port: -1}}
	} else {
		e.Subsets[0].Ports = make([]EndpointPort, len(ends.Ports))
		for k, p := range ends.Ports {
			port := int32(-1)
			name := ""
			protocol := ""
			if p.Port != nil {
				port = *p.Port
			}
			if p.Name != nil {
				name = *p.Name
			}
			if p.Protocol != nil {
				protocol = string(*p.Protocol)
			}
			ep := EndpointPort{Port: port, Name: name, Protocol: protocol}
			e.Subsets[0].Ports[k] = ep
		}
	}

	for _, end := range ends.Endpoints {
		if !endpointslicePublish(end.Conditions, opts.IncludeTerminating) {
			continue
		}
		for _, a := range end.Addresses {
			ea := EndpointAddress{IP: a}
			if end.Hostname != nil {
				ea.Hostname = *end.Hostname
			}
			if opts.WithZones && end.Zone != nil {
				if e.Zones == nil {
					e.Zones = make(map[string]string)
				}
				// Lowercased once here: qnames arrive case-folded, so
				// lookups compare without folding per query.
				e.Zones[a] = strings.ToLower(*end.Zone)
			}
			// ignore pod names that are too long to be a valid label
			if end.TargetRef != nil && len(end.TargetRef.Name) < 64 {
				ea.TargetRefName = end.TargetRef.Name
			}
			if end.NodeName != nil {
				ea.NodeName = *end.NodeName
			}
			e.Subsets[0].Addresses = append(e.Subsets[0].Addresses, ea)
			e.IndexIP = append(e.IndexIP, a)
		}
	}

	*ends = discovery.EndpointSlice{}

	return e, nil
}

// endpointslicePublish reports whether an endpoint with these conditions
// belongs in DNS.
//
// Ready is what the plugin has always filtered on. The API requires it to be
// false for a terminating endpoint - except where the Service overrides
// readiness with publishNotReadyAddresses - so filtering on it alone drops a
// pod for the whole of its graceful shutdown, and a client holding the name
// cannot tell that endpoint from one that is gone.
//
// Serving is defined as identical to Ready except that it is set regardless of
// the terminating state, which makes it exactly the condition to filter on when
// terminating endpoints are wanted: an endpoint that is shutting down but still
// answering has Serving true and Ready false, and one that has stopped serving
// has both false. Terminating itself is never read, because it cannot change
// the outcome for any combination the API can produce.
func endpointslicePublish(c discovery.EndpointConditions, includeTerminating bool) bool {
	if includeTerminating {
		return endpointsliceServing(c)
	}
	return endpointsliceReady(c.Ready)
}

func endpointsliceReady(ready *bool) bool {
	// Per API docs: a nil value indicates an unknown state. In most cases consumers
	// should interpret this unknown state as ready.
	if ready == nil {
		return true
	}
	return *ready
}

func endpointsliceServing(c discovery.EndpointConditions) bool {
	// Per API docs: a nil serving means the condition is not reported, and
	// consumers should defer to ready.
	if c.Serving == nil {
		return endpointsliceReady(c.Ready)
	}
	return *c.Serving
}

// CopyWithoutSubsets copies e, without the subsets.
func (e *Endpoints) CopyWithoutSubsets() *Endpoints {
	e1 := &Endpoints{
		Version:   e.Version,
		Name:      e.Name,
		Namespace: e.Namespace,
		Index:     e.Index,
		IndexIP:   make([]string, len(e.IndexIP)),
	}
	copy(e1.IndexIP, e.IndexIP)
	return e1
}

var _ runtime.Object = &Endpoints{}

// DeepCopyObject implements the ObjectKind interface.
func (e *Endpoints) DeepCopyObject() runtime.Object {
	e1 := &Endpoints{
		Version:   e.Version,
		Name:      e.Name,
		Namespace: e.Namespace,
		Index:     e.Index,
		IndexIP:   make([]string, len(e.IndexIP)),
		Subsets:   make([]EndpointSubset, len(e.Subsets)),
	}
	copy(e1.IndexIP, e.IndexIP)
	if e.Zones != nil {
		e1.Zones = maps.Clone(e.Zones)
	}

	for i, eps := range e.Subsets {
		sub := EndpointSubset{
			Addresses: make([]EndpointAddress, len(eps.Addresses)),
			Ports:     make([]EndpointPort, len(eps.Ports)),
		}
		for j, a := range eps.Addresses {
			ea := EndpointAddress{IP: a.IP, Hostname: a.Hostname, NodeName: a.NodeName, TargetRefName: a.TargetRefName}
			sub.Addresses[j] = ea
		}
		for k, p := range eps.Ports {
			ep := EndpointPort{Port: p.Port, Name: p.Name, Protocol: p.Protocol}
			sub.Ports[k] = ep
		}
		e1.Subsets[i] = sub
	}
	return e1
}

// GetNamespace implements the metav1.Object interface.
func (e *Endpoints) GetNamespace() string { return e.Namespace }

// SetNamespace implements the metav1.Object interface.
func (e *Endpoints) SetNamespace(_namespace string) {}

// GetName implements the metav1.Object interface.
func (e *Endpoints) GetName() string { return e.Name }

// SetName implements the metav1.Object interface.
func (e *Endpoints) SetName(_name string) {}

// GetResourceVersion implements the metav1.Object interface.
func (e *Endpoints) GetResourceVersion() string { return e.Version }

// SetResourceVersion implements the metav1.Object interface.
func (e *Endpoints) SetResourceVersion(_version string) {}
