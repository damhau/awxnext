package resources

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/agent"
	"github.com/damhau/awxnext/internal/api"
)

// HealthCheckTimeout is how long an execution node's agent has to answer a
// health check before the node is marked unavailable.
var HealthCheckTimeout = 15 * time.Second

// healthCheckFields are the fields of AWX's InstanceHealthCheckSerializer.
var healthCheckFields = []string{"uuid", "hostname", "ip_address", "version", "last_health_check", "errors", "cpu", "memory",
	"cpu_capacity", "mem_capacity", "capacity"}

// instanceHealthCheck is /instances/N/health_check/: GET returns the last
// result, POST runs a check, as AWX's InstanceHealthCheck for superusers and
// auditors (who only read). An execution node's agent is asked to report its
// resources; a control node probes itself (the database and the projects
// directory), which AWX does not offer.
func instanceHealthCheck(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
	if !c.User.Superuser && (!c.User.Auditor || r.Method != "GET") {
		api.WriteError(w, api.ErrForbidden)
		return
	}
	id, _ := api.ID(obj["id"])
	host, nodeType := str(obj["hostname"]), str(obj["node_type"])
	if r.Method == "GET" {
		inst := api.Lookup("instances")
		cur, err := api.Get(c, inst, id)
		if err == nil {
			cur, err = api.SerializeOne(c, inst, cur)
		}
		if err != nil {
			api.WriteError(w, err)
			return
		}
		out := Obj{}
		for _, f := range healthCheckFields {
			out[f] = cur[f]
		}
		api.WriteJSON(w, 200, out)
		return
	}
	if nodeType == "hop" {
		api.WriteJSON(w, 400, Obj{"error": fmt.Sprintf("Cannot run a health check on instances of type %s.  Health checks can only be run on execution nodes.", nodeType)})
		return
	}
	var pending bool
	c.DB().QueryRow(c, "SELECT health_check_started IS NOT NULL AND (last_health_check IS NULL OR health_check_started > last_health_check) FROM instances WHERE id = $1", id).Scan(&pending)
	if pending {
		api.WriteJSON(w, 200, Obj{"msg": fmt.Sprintf("Health check was already in progress for %s.", host)})
		return
	}
	if _, err := c.DB().Exec(c, "UPDATE instances SET health_check_started = now() WHERE id = $1", id); err != nil {
		api.WriteError(w, err)
		return
	}
	var err error
	switch {
	case nodeType == "execution":
		if agent.Default != nil {
			err = agent.Default.RequestHealthCheck(c, host)
		}
		if err == nil {
			go agentHealthCheckTimeout(c.App, id)
		}
	case host == c.App.Cfg.NodeName:
		err = probeThisNode(c, id)
	default:
		err = judgeControlNode(c, id)
	}
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, Obj{"msg": fmt.Sprintf("Health check is running for %s.", host)})
}

// agentHealthCheckTimeout marks the node unavailable, as AWX marks an
// unreachable node offline, when its agent has not answered in time (it is
// not connected, or predates health checks).
func agentHealthCheckTimeout(app *api.App, id int64) {
	time.Sleep(HealthCheckTimeout)
	app.DB.Exec(context.Background(), `UPDATE instances SET errors = 'The agent did not answer the health check: it is not connected.',
		capacity = 0, cpu_capacity = 0, mem_capacity = 0, node_state = 'unavailable', last_health_check = now()
		WHERE id = $1 AND health_check_started IS NOT NULL AND (last_health_check IS NULL OR last_health_check < health_check_started)`, id)
}

// probeThisNode checks what this control node needs to run jobs and
// records its resources.
func probeThisNode(c *Ctx, id int64) error {
	var errs []string
	if err := c.App.DB.Ping(c); err != nil {
		errs = append(errs, "database: "+err.Error())
	}
	if dir := c.App.Cfg.ProjectsRoot; dir != "" {
		if f, err := os.CreateTemp(dir, ".health-check-*"); err != nil {
			errs = append(errs, "projects directory: "+err.Error())
		} else {
			f.Close()
			os.Remove(f.Name())
		}
	}
	cpu, mem := runtime.NumCPU(), agent.TotalMemory()
	cpuCap, memCap := cpu*4, int(max((mem>>20)-2048, 0)/100)
	capacity := c.App.Cfg.Capacity
	if len(errs) > 0 {
		capacity = 0
	}
	_, err := c.DB().Exec(c, `UPDATE instances SET cpu = $2, memory = $3, cpu_capacity = $4, mem_capacity = $5, capacity = $6, errors = $7,
		version = $8, last_health_check = now(), last_seen = now() WHERE id = $1`,
		id, cpu, mem, cpuCap, memCap, capacity, strings.Join(errs, "\n"), api.ProductVersion)
	return err
}

// judgeControlNode checks another control node by its heartbeat (every 15
// seconds): the probe itself runs only on the node it checks.
func judgeControlNode(c *Ctx, id int64) error {
	_, err := c.DB().Exec(c, `UPDATE instances SET last_health_check = now(),
		errors = CASE WHEN last_seen > now() - interval '1 minute' THEN '' ELSE 'The node has not been seen for over a minute.' END,
		capacity = CASE WHEN last_seen > now() - interval '1 minute' THEN capacity ELSE 0 END WHERE id = $1`, id)
	return err
}
