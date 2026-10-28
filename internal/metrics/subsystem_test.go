package metrics

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSubsystemHistogram(t *testing.T) {
	s := &Subsystem{vals: map[string]float64{}, hists: map[string]*subHist{}}
	for _, v := range []float64{5, 40, 40, 5000} {
		s.Observe("callback_receiver_batch_events_insert_db", v)
	}
	s.Add("callback_receiver_events_insert_db", 3)
	var snap map[string]any
	b, _ := json.Marshal(s.Snapshot())
	json.Unmarshal(b, &snap)
	var out strings.Builder
	WriteSubsystem(&out, map[string]map[string]any{"n1": snap}, []string{"callback_receiver_batch_events_insert_db", "callback_receiver_events_insert_db"})
	for _, want := range []string{
		`callback_receiver_batch_events_insert_db_bucket{le="10",node="n1"} 1`,
		`callback_receiver_batch_events_insert_db_bucket{le="50",node="n1"} 3`,
		`callback_receiver_batch_events_insert_db_bucket{le="2000",node="n1"} 3`,
		`callback_receiver_batch_events_insert_db_bucket{le="+Inf",node="n1"} 4`,
		`callback_receiver_batch_events_insert_db_sum{node="n1"} 5085`,
		`callback_receiver_events_insert_db{node="n1"} 3`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %s in:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "task_manager") {
		t.Error("a metric outside the filter was written")
	}
}
