package routing_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/zalando/skipper/eskip"
	"github.com/zalando/skipper/filters"
	"github.com/zalando/skipper/filters/builtin"
	"github.com/zalando/skipper/filters/filtertest"
	"github.com/zalando/skipper/logging/loggingtest"
	"github.com/zalando/skipper/routing"
	"github.com/zalando/skipper/routing/testdataclient"
)

const (
	pollTimeout     = 15 * time.Millisecond
	predicateHeader = "X-Custom-Predicate"
)

type predicate struct {
	matchVal string
}

type testRouting struct {
	log     *loggingtest.Logger
	routing *routing.Routing
}

func (cp *predicate) Name() string { return "CustomPredicate" }

func (cp *predicate) Create(args []interface{}) (routing.Predicate, error) {
	if len(args) != 1 {
		return nil, errors.New("invalid number of args")
	}

	if matchVal, ok := args[0].(string); ok {
		cp.matchVal = matchVal
		return &predicate{matchVal}, nil
	} else {
		return nil, errors.New("invalid arg")
	}
}

func (cp *predicate) Match(r *http.Request) bool {
	return r.Header.Get(predicateHeader) == cp.matchVal
}

func newTestRoutingWithFiltersPredicates(fr filters.Registry, cps []routing.PredicateSpec, dc ...routing.DataClient) (*testRouting, error) {
	tl := loggingtest.New()
	rt := routing.New(routing.Options{
		FilterRegistry: fr,
		Predicates:     cps,
		DataClients:    dc,
		PollTimeout:    pollTimeout,
		Log:            tl})
	tr := &testRouting{tl, rt}
	return tr, tr.waitForNRouteSettings(len(dc))
}

func newTestRoutingWithFilters(fr filters.Registry, dc ...routing.DataClient) (*testRouting, error) {
	return newTestRoutingWithFiltersPredicates(fr, nil, dc...)
}

func newTestRoutingWithPredicates(cps []routing.PredicateSpec, dc ...routing.DataClient) (*testRouting, error) {
	return newTestRoutingWithFiltersPredicates(builtin.MakeRegistry(), cps, dc...)
}

func newTestRouting(dc ...routing.DataClient) (*testRouting, error) {
	return newTestRoutingWithFiltersPredicates(builtin.MakeRegistry(), nil, dc...)
}

func (tr *testRouting) waitForNRouteSettingsTO(n int, to time.Duration) error {
	return tr.log.WaitForN("route settings applied", n, to)
}

func (tr *testRouting) waitForNRouteSettings(n int) error {
	return tr.waitForNRouteSettingsTO(n, 12*pollTimeout)
}

func (tr *testRouting) waitForRouteSetting() error {
	return tr.waitForNRouteSettings(1)
}

func (tr *testRouting) checkRequest(req *http.Request) (*routing.Route, error) {
	if r, _ := tr.routing.Route(req); r != nil {
		return r, nil
	}

	return nil, errors.New("requested route not found")
}

func (tr *testRouting) checkGetRequest(url string) (*routing.Route, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	return tr.checkRequest(req)
}

func (tr *testRouting) close() {
	tr.log.Close()
	tr.routing.Close()
}

func TestKeepsReceivingInitialRouteDataUntilSucceeds(t *testing.T) {
	dc := testdataclient.New([]*eskip.Route{{Id: "route1", Path: "/some-path", Backend: "https://www.example.org"}})
	dc.FailNext()
	dc.FailNext()
	dc.FailNext()

	tr, err := newTestRouting(dc)
	if err != nil {
		t.Error(err)
		return
	}

	defer tr.close()

	if _, err := tr.checkGetRequest("https://www.example.com/some-path"); err != nil {
		t.Error(err)
	}
}

func TestReceivesInitial(t *testing.T) {
	dc := testdataclient.New([]*eskip.Route{{Id: "route1", Path: "/some-path", Backend: "https://www.example.org"}})
	tr, err := newTestRouting(dc)
	if err != nil {
		t.Error(err)
	}

	defer tr.close()

	if _, err := tr.checkGetRequest("https://www.example.com/some-path"); err != nil {
		t.Error(err)
	}
}

func TestReceivesFullOnFailedUpdate(t *testing.T) {
	dc := testdataclient.New([]*eskip.Route{{Id: "route1", Path: "/some-path", Backend: "https://www.example.org"}})
	tr, err := newTestRouting(dc)
	if err != nil {
		t.Error(err)
		return
	}

	defer tr.close()

	tr.log.Reset()
	dc.FailNext()
	dc.Update([]*eskip.Route{{Id: "route2", Path: "/some-other", Backend: "https://other.example.org"}}, nil)

	if err := tr.waitForRouteSetting(); err != nil {
		t.Error(err)
		return
	}

	if _, err := tr.checkGetRequest("https://www.example.com/some-other"); err != nil {
		t.Error(err)
	}
}

func TestReceivesUpdate(t *testing.T) {
	dc := testdataclient.New([]*eskip.Route{{Id: "route1", Path: "/some-path", Backend: "https://www.example.org"}})
	tr, err := newTestRouting(dc)
	if err != nil {
		t.Error(err)
		return
	}

	defer tr.close()

	tr.log.Reset()
	dc.Update([]*eskip.Route{{Id: "route2", Path: "/some-other", Backend: "https://other.example.org"}}, nil)

	if err := tr.waitForRouteSetting(); err != nil {
		t.Error(err)
		return
	}

	if _, err := tr.checkGetRequest("https://www.example.com/some-other"); err != nil {
		t.Error(err)
	}
}

func TestReceivesDelete(t *testing.T) {
	dc := testdataclient.New([]*eskip.Route{
		{Id: "route1", Path: "/some-path", Backend: "https://www.example.org"},
		{Id: "route2", Path: "/some-other", Backend: "https://other.example.org"}})
	tr, err := newTestRouting(dc)
	if err != nil {
		t.Error(err)
		return
	}

	defer tr.close()

	tr.log.Reset()
	dc.Update(nil, []string{"route1"})

	if err := tr.waitForRouteSetting(); err != nil {
		t.Error(err)
		return
	}

	if _, err := tr.checkGetRequest("https://www.example.com/some-path"); err == nil {
		t.Error("failed to delete")
	}
}

func TestUpdateDoesNotChangeRouting(t *testing.T) {
	dc := testdataclient.New([]*eskip.Route{{Id: "route1", Path: "/some-path", Backend: "https://www.example.org"}})
	tr, err := newTestRouting(dc)
	if err != nil {
		t.Error(err)
		return
	}

	defer tr.close()

	tr.log.Reset()
	dc.Update(nil, nil)

	if err := tr.waitForNRouteSettingsTO(1, 3*pollTimeout); err != nil && err != loggingtest.ErrWaitTimeout {
		t.Error(err)
		return
	}

	if _, err := tr.checkGetRequest("https://www.example.com/some-path"); err != nil {
		t.Error(err)
	}
}

func TestMergesMultipleSources(t *testing.T) {
	dc1 := testdataclient.New([]*eskip.Route{{Id: "route1", Path: "/some-path", Backend: "https://www.example.org"}})
	dc2 := testdataclient.New([]*eskip.Route{{Id: "route2", Path: "/some-other", Backend: "https://other.example.org"}})
	dc3 := testdataclient.New([]*eskip.Route{{Id: "route3", Path: "/another", Backend: "https://another.example.org"}})
	tr, err := newTestRouting(dc1, dc2, dc3)
	if err != nil {
		t.Error(err)
		return
	}

	defer tr.close()

	if _, err := tr.checkGetRequest("https://www.example.com/some-path"); err != nil {
		t.Error(err)
	}

	if _, err := tr.checkGetRequest("https://www.example.com/some-other"); err != nil {
		t.Error(err)
	}

	if _, err := tr.checkGetRequest("https://www.example.com/another"); err != nil {
		t.Error(err)
	}
}

func TestMergesUpdatesFromMultipleSources(t *testing.T) {
	dc1 := testdataclient.New([]*eskip.Route{{Id: "route1", Path: "/some-path", Backend: "https://www.example.org"}})
	dc2 := testdataclient.New([]*eskip.Route{{Id: "route2", Path: "/some-other", Backend: "https://other.example.org"}})
	dc3 := testdataclient.New([]*eskip.Route{{Id: "route3", Path: "/another", Backend: "https://another.example.org"}})
	tr, err := newTestRouting(dc1, dc2, dc3)
	if err != nil {
		t.Error(err)
		return
	}

	defer tr.close()

	if _, err := tr.checkGetRequest("https://www.example.com/some-path"); err != nil {
		t.Error(err)
	}

	if _, err := tr.checkGetRequest("https://www.example.com/some-other"); err != nil {
		t.Error(err)
	}

	if _, err := tr.checkGetRequest("https://www.example.com/another"); err != nil {
		t.Error(err)
	}

	tr.log.Reset()

	dc1.Update([]*eskip.Route{{Id: "route1", Path: "/some-changed-path", Backend: "https://www.example.org"}}, nil)
	dc2.Update([]*eskip.Route{{Id: "route2", Path: "/some-other-changed", Backend: "https://www.example.org"}}, nil)
	dc3.Update(nil, []string{"route3"})

	if err := tr.waitForNRouteSettings(3); err != nil {
		t.Error(err)
		return
	}

	if _, err := tr.checkGetRequest("https://www.example.com/some-changed-path"); err != nil {
		t.Error(err)
	}

	if _, err := tr.checkGetRequest("https://www.example.com/some-other-changed"); err != nil {
		t.Error(err)
	}

	if _, err := tr.checkGetRequest("https://www.example.com/another"); err == nil {
		t.Error(err)
	}
}

func TestIgnoresInvalidBackend(t *testing.T) {
	dc := testdataclient.New([]*eskip.Route{{Id: "route1", Path: "/some-path", Backend: "invalid backend"}})
	tr, err := newTestRouting(dc)
	if err != nil {
		t.Error(err)
	}

	defer tr.close()

	if err = tr.log.WaitFor("invalid backend", time.Second); err != nil {
		t.Error(err)
	}
}

func TestProcessesFilterDefinitions(t *testing.T) {
	fr := make(filters.Registry)
	fs := &filtertest.Filter{FilterName: "filter1"}
	fr.Register(fs)

	dc := testdataclient.New([]*eskip.Route{{
		Id:      "route1",
		Path:    "/some-path",
		Filters: []*eskip.Filter{{Name: "filter1", Args: []interface{}{3.14, "Hello, world!"}}},
		Backend: "https://www.example.org"}})

	tr, err := newTestRoutingWithFilters(fr, dc)
	if err != nil {
		t.Error(err)
		return
	}

	defer tr.close()

	if r, err := tr.checkGetRequest("https://www.example.com/some-path"); r == nil || err != nil {
		t.Error(err)
	} else {
		if len(r.Filters) != 1 {
			t.Error("failed to process filters")
			return
		}

		if f, ok := r.Filters[0].Filter.(*filtertest.Filter); !ok ||
			f.FilterName != fs.Name() || len(f.Args) != 2 ||
			f.Args[0] != float64(3.14) || f.Args[1] != "Hello, world!" {
			t.Error("failed to process filters")
		}
	}
}

func TestProcessesPredicates(t *testing.T) {
	dc, err := testdataclient.NewDoc(`
        route1: CustomPredicate("custom1") -> "https://route1.example.org";
        route2: CustomPredicate("custom2") -> "https://route2.example.org";
        catchAll: * -> "https://route.example.org"`)
	if err != nil {
		t.Error(err)
		return
	}

	cps := []routing.PredicateSpec{&predicate{}, &predicate{}}

	tr, err := newTestRoutingWithPredicates(cps, dc)
	if err != nil {
		t.Error(err)
		return
	}

	defer tr.close()

	req, err := http.NewRequest("GET", "https://www.example.com", nil)
	if err != nil {
		t.Error(err)
		return
	}

	req.Header.Set(predicateHeader, "custom1")
	if r, err := tr.checkRequest(req); r == nil || err != nil {
		t.Error(err)
	} else {
		if r.Backend != "https://route1.example.org" {
			t.Error("custom predicate matching failed, route1")
			return
		}
	}

	req.Header.Del(predicateHeader)
	if r, err := tr.checkRequest(req); r == nil || err != nil {
		t.Error(err)
	} else {
		if r.Backend != "https://route.example.org" {
			t.Error("custom predicate matching failed, catch-all")
			return
		}
	}
}

// TestNonMatchedStaticRoute for bug #116: non-matched static route supress wild-carded route
func TestNonMatchedStaticRoute(t *testing.T) {
	dc, err := testdataclient.NewDoc(`
		a: Path("/foo/*_") -> "https://foo.org";
		b: Path("/foo/bar") && CustomPredicate("custom1") -> "https://bar.org";
		z: * -> "https://catch.all"`)
	if err != nil {
		t.Error(err)
		return
	}

	cps := []routing.PredicateSpec{&predicate{}}

	tr, err := newTestRoutingWithPredicates(cps, dc)
	if err != nil {
		t.Error(err)
		return
	}

	defer tr.close()

	req, err := http.NewRequest("GET", "https://www.example.com/foo/bar", nil)
	if err != nil {
		t.Error(err)
		return
	}

	if r, err := tr.checkRequest(req); r == nil || err != nil {
		t.Error(err)
	} else {
		if r.Backend != "https://foo.org" {
			t.Error("non-matched static route supress wild-carded route")
		}
	}
}
