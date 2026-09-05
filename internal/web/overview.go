package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/matbour/sandbox-agent/internal/job"
)

type jobView struct {
	job.State
	Short   string // model without the "claude-" prefix
	Time    string
	Cost    string
	Running bool
	Detail  string
}

func (s *Server) views() ([]jobView, error) {
	jobs, err := job.Jobs(s.StateDir)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]jobView, 0, len(jobs))
	for _, j := range jobs {
		v := jobView{State: j, Short: strings.TrimPrefix(j.Model, "claude-"), Time: j.Elapsed(now).String(),
			Running: !j.Terminal()}
		if j.CostUSD > 0 {
			v.Cost = fmt.Sprintf("$%.2f", j.CostUSD)
		}
		switch j.Stage {
		case job.StageFailed:
			v.Detail = strings.Join(strings.Fields(j.Error), " ")
			if r := []rune(v.Detail); len(r) > 160 {
				v.Detail = string(r[:159]) + "…"
			}
		case job.StageInterrupted:
			v.Detail = "agentd stopped during the job"
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) apiJobs(w http.ResponseWriter, _ *http.Request) {
	jobs, err := job.Jobs(s.StateDir)
	if err != nil {
		http.Error(w, "cannot read jobs", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jobs": jobs})
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.views()
	if err != nil {
		http.Error(w, "cannot read jobs", http.StatusInternalServerError)
		return
	}
	var running int
	var cost float64
	for _, j := range jobs {
		if j.Running {
			running++
		}
		cost += j.CostUSD
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = page.Execute(w, map[string]any{
		"Jobs": jobs, "Running": running, "Total": len(jobs), "Cost": fmt.Sprintf("$%.2f", cost),
		"User": r.Header.Get("X-Agentd-User"), "Now": time.Now().Format("15:04:05"),
	})
}

var page = template.Must(template.New("overview").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="10">
<title>sandbox-agent</title>
<style>
:root { --bg:#fafafa; --fg:#1a1a1a; --muted:#6b6b6b; --line:#e3e3e3; --card:#fff;
  --run:#2563eb; --ok:#15803d; --bad:#b91c1c; --warn:#b45309; }
@media (prefers-color-scheme: dark) { :root { --bg:#111; --fg:#e8e8e8; --muted:#9a9a9a; --line:#2a2a2a;
  --card:#181818; --run:#60a5fa; --ok:#4ade80; --bad:#f87171; --warn:#fbbf24; } }
* { box-sizing: border-box; }
body { margin:0; padding:24px 16px; background:var(--bg); color:var(--fg);
  font:14px/1.45 ui-sans-serif, system-ui, -apple-system, sans-serif; }
main { max-width:1100px; margin:0 auto; }
header { display:flex; flex-wrap:wrap; gap:8px 24px; align-items:baseline; margin-bottom:16px; }
h1 { font-size:18px; margin:0; }
.stats { color:var(--muted); }
.stats b { color:var(--fg); }
.wrap { overflow-x:auto; background:var(--card); border:1px solid var(--line); border-radius:8px; }
table { width:100%; border-collapse:collapse; }
th, td { text-align:left; padding:8px 12px; border-bottom:1px solid var(--line); vertical-align:top; }
th { font-weight:600; color:var(--muted); font-size:12px; text-transform:uppercase; letter-spacing:.03em; }
tr:last-child td { border-bottom:0; }
td.num { font-variant-numeric: tabular-nums; white-space:nowrap; }
.stage { font-weight:600; white-space:nowrap; }
.run { color:var(--run); } .done { color:var(--ok); } .failed { color:var(--bad); } .interrupted { color:var(--warn); }
.title { color:var(--muted); }
.detail { color:var(--bad); font-size:13px; }
a { color:inherit; }
footer { color:var(--muted); font-size:12px; margin-top:12px; }
</style>
</head>
<body>
<main>
<header>
  <h1>sandbox-agent</h1>
  <span class="stats"><b>{{.Running}}</b> running · <b>{{.Total}}</b> jobs · <b>{{.Cost}}</b> agent cost</span>
</header>
<div class="wrap">
<table>
<thead><tr><th>Issue</th><th>Stage</th><th>Model</th><th>Started</th><th>Time</th><th>Cost</th><th>Result</th></tr></thead>
<tbody>
{{range .Jobs}}
<tr>
  <td>{{if .URL}}<a href="{{.URL}}" rel="noreferrer">{{.Issue}}</a>{{else}}{{.Issue}}{{end}}<br><span class="title">{{.Title}}</span></td>
  <td class="stage {{if .Running}}run{{else}}{{.Stage}}{{end}}">{{.Stage}}</td>
  <td>{{.Short}}</td>
  <td class="num">{{.Started.Local.Format "Jan 02 15:04"}}</td>
  <td class="num">{{.Time}}</td>
  <td class="num">{{.Cost}}</td>
  <td>{{if .PR}}<a href="{{.PR}}" rel="noreferrer">pull request</a>{{if .Result}} · {{.Result}}{{end}}{{end}}{{if .Detail}}<span class="detail">{{.Detail}}</span>{{end}}</td>
</tr>
{{else}}
<tr><td colspan="7">No jobs yet.</td></tr>
{{end}}
</tbody>
</table>
</div>
<footer>Refreshes every 10 s · {{.Now}}{{if .User}} · {{.User}}{{end}}</footer>
</main>
</body>
</html>
`))
