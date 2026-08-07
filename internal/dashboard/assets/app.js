const token = document.querySelector('meta[name="workbench-token"]').content;
const state = { snapshot: null, projectId: null, taskId: null };
const $ = id => document.getElementById(id);
const esc = value => String(value ?? "").replace(/[&<>"']/g, character => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[character]);

function notice(message, error = false) {
  const element = $("notice");
  element.textContent = message;
  element.className = `notice${error ? " error" : ""}`;
  element.hidden = false;
  window.clearTimeout(notice.timer);
  notice.timer = window.setTimeout(() => { element.hidden = true; }, 5000);
}

function isActiveTask(task) {
  return ["active", "pending", "starting", "running", "waiting", "idle"].includes(task.lifecycle);
}

function taskCard(task) {
  const kind = task.kind || task.agent_kind || "task";
  const lifecycle = task.lifecycle || task.state || "unknown";
  const provenance = task.provenance || "managed";
  const location = task.runtime_location?.pane_id || task.managed?.backend || task.backend || "unknown";
  return `<button class="agent-card ${task.id === state.taskId ? "selected" : ""}" data-task="${esc(task.id)}"><span class="agent-head"><span class="avatar">${esc(kind[0])}</span><span class="task-badges"><span class="provenance ${esc(provenance)}">${esc(provenance)}</span><span class="status ${esc(lifecycle)}">${esc(lifecycle)}</span></span></span><strong>${esc(kind)}</strong><small>${esc(location)} · ${esc(task.id)}</small></button>`;
}

async function load() {
  try {
    const response = await fetch("/api/v1/snapshot", { headers: { Accept: "application/json" } });
    const body = await response.json();
    if (!response.ok || !body.ok) throw new Error(body.error?.message || `HTTP ${response.status}`);
    state.snapshot = body.data;
    if (!state.projectId || !body.data.projects.some(project => project.id === state.projectId)) {
      state.projectId = body.data.projects[0]?.id || null;
    }
    render();
  } catch (error) {
    notice(`Snapshot failed: ${error.message}`, true);
  }
}

async function action(payload) {
  try {
    const response = await fetch("/api/v1/actions", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Workbench-Token": token },
      body: JSON.stringify(payload),
    });
    const body = await response.json();
    if (!response.ok || !body.ok) throw new Error(body.error?.message || `HTTP ${response.status}`);
    notice(body.data.message);
    await load();
  } catch (error) {
    notice(error.message, true);
  }
}

function bindTaskCards() {
  document.querySelectorAll("[data-task]").forEach(button => {
    button.onclick = () => {
      state.taskId = button.dataset.task;
      renderTask();
      document.querySelectorAll("[data-task]").forEach(card => card.classList.toggle("selected", card.dataset.task === state.taskId));
    };
  });
}

function render() {
	const data = state.snapshot;
  const tasks = data.tasks || data.agents || [];
  $("platform").textContent = data.platform;
  $("profile").textContent = data.profile;
  $("project-count").textContent = data.projects.length;

  const missing = data.doctor.summary.unavailable_core;
  $("health-title").textContent = missing ? `${missing} core checks missing` : "Core ready";
  $("health-detail").textContent = `${data.doctor.summary.available} available · ${data.doctor.summary.unavailable_optional} optional missing`;
  document.querySelector(".pulse").classList.toggle("ok", !missing);

  renderTmux(data.tmux || { available: false, reason: "tmux observation unavailable", sessions: [] });
  renderScheduler(data.scheduler || { available: false, running: false, reason: "scheduler unavailable", jobs: [] });
  renderUnregisteredTasks(tasks);
  renderOverview(data.overview || { counts: {}, attention: [], work_locations: [], tool_health: data.tool_health || { available: false, capabilities: [], summary: {} } });

  $("projects").innerHTML = data.projects.map(project => {
    const activeCount = tasks.filter(task => task.project_id === project.id && isActiveTask(task)).length;
    return `<button class="project ${project.id === state.projectId ? "active" : ""}" data-project="${esc(project.id)}"><span class="project-dot"></span><span class="project-name">${esc(project.name)}</span><span class="project-meta">${activeCount}</span></button>`;
  }).join("");
  document.querySelectorAll("[data-project]").forEach(button => {
    button.onclick = () => {
      state.projectId = button.dataset.project;
      state.taskId = null;
      render();
    };
  });

  const project = data.projects.find(item => item.id === state.projectId);
  $("empty").hidden = Boolean(project);
  $("workspace").hidden = !project;
  if (!project) {
    renderTask();
    return;
  }

  $("project-name").textContent = project.name;
  $("project-path").textContent = project.path;
  const projectTasks = tasks.filter(task => task.project_id === project.id);
  const activeTasks = projectTasks.filter(isActiveTask);
  const historyTasks = projectTasks.filter(task => !isActiveTask(task));
  const worktrees = data.worktrees.filter(item => item.project_id === project.id);
  const changes = data.changes.find(item => item.project_id === project.id) || { changed: 0, branch: "unknown", changed_files: [] };
  const workflows = (data.workflows || []).filter(item => item.project_id === project.id);
  const workflowHistory = (data.workflow_history || []).filter(item => item.project_id === project.id);

  renderContexts(data.contexts || { registry_available: false, reason: "Context registry unavailable", environments: [] }, project);

  $("metric-agents").textContent = activeTasks.length;
  $("active-agent-count").textContent = activeTasks.length;
  $("metric-worktrees").textContent = worktrees.length;
  $("metric-changes").textContent = changes.changed;
  $("metric-branch").textContent = changes.branch || "branch unavailable";

  $("agents").innerHTML = activeTasks.length ? activeTasks.map(taskCard).join("") : '<div class="row"><span>No active tasks</span></div>';
  $("history-count").textContent = historyTasks.length;
  $("agent-registry-path").textContent = data.agent_registry_path || "agents.json";
  $("agent-history-list").innerHTML = historyTasks.length ? historyTasks.map(taskCard).join("") : '<div class="row"><span>No terminal task history</span></div>';
  $("clear-agent-history").disabled = historyTasks.length === 0;
  bindTaskCards();

  $("worktrees").innerHTML = worktrees.length ? worktrees.map(item => `<div class="row"><strong title="${esc(item.path)}">${esc(item.branch || item.id)}</strong><span>${item.dirty ? "dirty" : "clean"}${item.managed ? " · managed" : ""}</span></div>`).join("") : '<div class="row"><span>No linked worktrees</span></div>';
  $("changes").innerHTML = changes.unavailable ? `<div class="row"><span>${esc(changes.unavailable)}</span></div>` : changes.changed_files.length ? changes.changed_files.slice(0, 8).map(file => `<div class="row"><strong title="${esc(file)}">${esc(file)}</strong><span>changed</span></div>`).join("") : '<div class="row"><span>Working tree clean</span></div>';
  const capabilities = data.doctor.capabilities.filter(capability => capability.scope === "core" || capability.status === "unavailable").slice(0, 8);
  $("doctor").innerHTML = capabilities.map(capability => `<div class="row"><strong>${esc(capability.name)}</strong><span>${esc(capability.status)}</span></div>`).join("");
  $("workflows").innerHTML = workflows.length ? workflows.map(workflow => `<button type="button" class="workflow-action" data-workflow-id="${esc(workflow.id)}" ${workflow.status === "available" ? "" : "disabled"}><strong>${esc(workflow.name)}</strong><span class="status ${esc(workflow.status)}">${esc(workflow.status)}</span><small>${esc(workflow.status === "available" ? `${workflow.description} · ${workflow.risk}` : workflow.reason)}</small></button>`).join("") : '<div class="row"><span>No workflow catalog entries</span></div>';
  document.querySelectorAll("[data-workflow-id]").forEach(button => {
    button.onclick = () => {
      if (button.disabled) return;
      const workflowId = button.dataset.workflowId;
      const workflow = workflows.find(item => item.id === workflowId);
      if (!window.confirm(`Run allowlisted workflow ${workflowId} for ${project.id}? Risk: ${workflow?.risk || "unknown"}.`)) return;
      action({ action: "run_workflow", project_id: project.id, workflow_id: workflowId });
    };
  });
  $("workflow-history").innerHTML = workflowHistory.length ? workflowHistory.slice(0, 8).map(run => `<div class="row"><strong>${esc(run.workflow_id)}</strong><span class="status ${esc(run.status)}">${esc(run.status)}</span><small>${esc(new Date(run.finished_at).toLocaleString())}${run.output_truncated ? " · output capped" : ""}</small></div>`).join("") : '<div class="row"><span>No workflow runs recorded</span></div>';
  renderTask();
}

function renderContexts(contexts, project) {
  const status = $("context-status");
  const target = $("contexts");
  const environments = Array.isArray(contexts?.environments) ? contexts.environments : [];
  if (!contexts?.registry_available) {
    status.textContent = "unavailable";
    status.className = "status unavailable";
    target.innerHTML = `<div class="context-state error"><strong>Context registry unavailable</strong><p>${esc(contexts?.reason || "Environment metadata could not be loaded.")}</p></div>`;
    return;
  }
  const linkedID = project.environment_id || "";
  const environment = environments.find(item => item.id === linkedID) || environments.find(item => (item.project_ids || []).includes(project.id));
  if (!linkedID && !environment) {
    status.textContent = "not linked";
    status.className = "status skipped";
    target.innerHTML = '<div class="context-state"><strong>No default environment</strong><p>This project runs without a Workbench environment unless the CLI explicitly selects one.</p></div>';
    return;
  }
  if (!environment) {
    status.textContent = "missing";
    status.className = "status warning";
    target.innerHTML = `<div class="context-state error"><strong>Linked environment is missing</strong><p>The project references ${esc(linkedID)}, but the registry has no matching environment.</p></div>`;
    return;
  }
  const exportKeys = environment.export_keys || [];
  const secretReferences = environment.secret_references || [];
  const missingSecrets = secretReferences.filter(item => item.status !== "available");
  const expiry = environment.expiry || { status: "permanent" };
  const expiryBlocked = expiry.status === "expired";
  status.textContent = expiryBlocked ? "expired" : (missingSecrets.length ? "review" : (expiry.status === "expiring" ? "expiring" : "available"));
  status.className = `status ${expiryBlocked || expiry.status === "expiring" || missingSecrets.length ? "warning" : "available"}`;
  const metadata = [
    ["Environment", environment.id],
    ["AWS profile", environment.aws_profile || "Not set"],
    ["AWS region", environment.aws_region || "Not set"],
    ["Kubernetes context", environment.kube_context || "Not set"],
    ["Kubernetes namespace", environment.kube_namespace || "Not set"],
    ["Expiry", expiry.expires_at ? `${expiry.status} · ${new Date(expiry.expires_at).toLocaleString()}` : "Permanent"],
  ];
  target.innerHTML = `<dl class="context-metadata">${metadata.map(([label, value]) => `<dt>${esc(label)}</dt><dd>${esc(value)}</dd>`).join("")}</dl><div class="context-group"><strong>Export keys</strong>${exportKeys.length ? `<div class="context-tags">${exportKeys.map(key => `<code>${esc(key)}</code>`).join("")}</div>` : '<p>No ordinary export keys</p>'}</div><div class="context-group"><strong>Secret references</strong>${secretReferences.length ? `<div class="context-secret-list">${secretReferences.map(item => `<div><code>${esc(item.variable)}</code><span class="status ${esc(item.status)}">${esc(item.status)}</span></div>`).join("")}</div>` : '<p>No secret references</p>'}</div>`;
}

function renderScheduler(scheduler) {
  const jobs = scheduler.jobs || [];
  $("scheduler-status").textContent = scheduler.available ? (scheduler.running ? "running" : "stopped") : "unavailable";
  $("scheduler-status").className = `status ${scheduler.available && scheduler.running ? "available" : "warning"}`;
  $("scheduler-availability").textContent = scheduler.available ? `${jobs.length} registered job${jobs.length === 1 ? "" : "s"}` : (scheduler.reason || "scheduler unavailable");
  $("scheduler-availability").classList.toggle("unavailable", !scheduler.available);
  $("scheduler-jobs").innerHTML = jobs.length ? jobs.map(job => {
    const details = Object.entries(job.details || {}).map(([key, value]) => `${key} ${value}`).join(" · ");
    const next = job.next_run_at ? new Date(job.next_run_at).toLocaleString() : "not scheduled";
    return `<div class="row"><strong>${esc(job.id)}</strong><span class="status ${esc(job.status)}">${esc(job.status)}</span><small>${esc(details || job.error || "No result yet")} · next ${esc(next)}</small></div>`;
  }).join("") : '<div class="row"><span>No scheduler jobs</span></div>';
}

function renderOverview(overview) {
  const counts = overview.counts || {};
  const attention = overview.attention || [];
  const locations = overview.work_locations || [];
  const tools = overview.tool_health || { available: false, capabilities: [], summary: {} };
  $("overview-managed").textContent = counts.active_managed_tasks || 0;
  $("overview-observed").textContent = counts.active_observed_tasks || 0;
  $("overview-sessions").textContent = counts.tmux_sessions || 0;
  $("overview-session-detail").textContent = `${counts.detached_sessions || 0} detached`;
  $("overview-attention-count").textContent = `${attention.length} attention`;
  $("overview-tool-health").textContent = tools.available ? (tools.summary?.unavailable_core ? "Review" : "Ready") : "Offline";
  $("overview-tool-detail").textContent = tools.available ? `${tools.summary?.available || 0} available · ${tools.summary?.unavailable_optional || 0} optional missing` : (tools.reason || "binbox unavailable");
  $("overview-attention").innerHTML = attention.length ? attention.slice(0, 8).map(item => `<div class="row attention-row"><strong>${esc(item.title)}</strong><span class="status ${esc(item.severity)}">${esc(item.kind)}</span><small>${esc(item.reason)}</small></div>`).join("") : '<div class="row"><span>No verified attention items</span></div>';
  $("overview-locations").innerHTML = locations.length ? locations.map(location => `<button type="button" class="row location-row" data-overview-task="${esc(location.task_id)}"><strong>${esc(location.kind)} · ${esc(location.project_id || "unregistered")}</strong><span>${location.can_jump ? "resumable" : "unavailable"}</span><small>${esc(location.session_name ? `${location.session_name}:${location.window_index} ${location.pane_id}` : location.path || location.task_id)}</small></button>`).join("") : '<div class="row"><span>No active work locations</span></div>';
  document.querySelectorAll("[data-overview-task]").forEach(button => {
    button.onclick = () => {
      state.taskId = button.dataset.overviewTask;
      renderTask();
    };
  });
  if (!tools.available) {
    $("overview-tools").innerHTML = `<div class="row"><strong>binbox</strong><span>unavailable</span><small>${esc(tools.reason || "health provider unavailable")}</small></div>`;
    return;
  }
  const capabilities = tools.capabilities || [];
  $("overview-tools").innerHTML = capabilities.length ? capabilities.map(tool => `<div class="row"><strong>${esc(tool.name)}</strong><span class="status ${esc(tool.status)}">${esc(tool.status)}</span><small>${esc(tool.status === "available" ? tool.description : tool.recovery || tool.reason)}</small></div>`).join("") : '<div class="row"><span>No tool capabilities reported</span></div>';
}

function renderUnregisteredTasks(tasks) {
  const unregistered = tasks.filter(task => !task.project_id && isActiveTask(task));
  $("unregistered-task-count").textContent = unregistered.length;
  $("unregistered-tasks").innerHTML = unregistered.length ? unregistered.map(taskCard).join("") : '<div class="row"><span>None observed</span></div>';
  bindTaskCards();
}

function renderTmux(tmux) {
  const sessions = tmux.sessions || [];
  $("tmux-session-count").textContent = sessions.length;
  $("tmux-availability").textContent = tmux.available ? "Live read-only snapshot from tmux" : (tmux.reason || "tmux unavailable");
  $("tmux-availability").classList.toggle("unavailable", !tmux.available);
  if (!tmux.available) {
    $("tmux-sessions").innerHTML = "";
    return;
  }
  const projectIDs = new Set((state.snapshot?.projects || []).map(project => project.id));
  $("tmux-sessions").innerHTML = sessions.map(session => {
    const ownership = session.ownership || "legacy";
    const projectID = session.project_id || (projectIDs.has(session.name) ? session.name : "");
    const canAdopt = ownership === "legacy" && projectID === session.name;
    const actions = `<div class="session-actions"><button type="button" data-session-action="attach_session" data-session-name="${esc(session.name)}">Attach</button>${canAdopt ? `<button type="button" data-session-action="adopt_session" data-project-id="${esc(projectID)}">Adopt</button>` : ""}${session.managed && projectID ? `<button type="button" class="danger" data-session-action="stop_session" data-project-id="${esc(projectID)}">Stop</button>` : ""}</div>`;
    const project = projectID ? ` · ${esc(projectID)}` : "";
    return `<section class="session-card"><div class="session-head"><strong>${esc(session.name)}</strong><span class="status ${esc(ownership)}">${esc(ownership)}</span></div><div class="session-meta"><span>${session.attached ? "attached" : "detached"} · ${esc(session.id)}${project}</span>${actions}</div>${session.windows.map(window => `<div class="window-row"><span>${window.index}:${esc(window.name)} · ${esc(window.id)}</span>${window.panes.map(pane => `<button type="button" class="pane-jump" data-pane-id="${esc(pane.id)}" title="Jump to ${esc(pane.id)}"><strong>${esc(pane.current_command || "shell")}</strong><small>${esc(pane.id)} · ${esc(pane.current_path)}</small></button>`).join("")}</div>`).join("")}</section>`;
  }).join("") || '<div class="row"><span>No tmux sessions</span></div>';
  document.querySelectorAll("[data-pane-id]").forEach(button => {
    button.onclick = () => action({ action: "jump_pane", pane_id: button.dataset.paneId });
  });
  document.querySelectorAll("[data-session-action]").forEach(button => {
    button.onclick = () => {
      const actionName = button.dataset.sessionAction;
      const projectID = button.dataset.projectId;
      if (actionName === "adopt_session" && !window.confirm(`Adopt tmux session ${projectID} as a Workbench-managed project session?`)) return;
      if (actionName === "stop_session" && !window.confirm(`Stop managed tmux session ${projectID}? All processes in the session will end.`)) return;
      const payload = { action: actionName };
      if (button.dataset.sessionName) payload.session_name = button.dataset.sessionName;
      if (projectID) payload.project_id = projectID;
      action(payload);
    };
  });
}

function renderTask() {
  const task = (state.snapshot?.tasks || state.snapshot?.agents || []).find(item => item.id === state.taskId);
  if (!task) state.taskId = null;
  $("task-empty").hidden = Boolean(task);
  $("task-detail").hidden = !task;
  if (!task) return;

  const kind = task.kind || task.agent_kind || "task";
  const lifecycle = task.lifecycle || task.state || "unknown";
  const provenance = task.provenance || "managed";
  $("task-kind").textContent = kind[0];
  $("task-id").textContent = task.id;
  $("task-state").textContent = lifecycle;
  $("task-state").className = `status ${lifecycle}`;
  const location = task.runtime_location || {};
  const managed = task.managed || task;
  const updated = task.last_observed_at || managed.last_event_at;
  const fields = [["Kind", kind], ["Provenance", provenance], ["Ownership", task.ownership || "managed"], ["Confidence", task.confidence || "authoritative"], ["Source", task.state_source || "registry"], ["Project", task.project_id || "unregistered"], ["Pane", location.pane_id || managed.backend_ref || "unavailable"], ["CWD", task.cwd], ["Exit result", task.exit_result || (task.exit_code == null ? "unknown" : `code ${task.exit_code}`)], ["Updated", updated ? new Date(updated).toLocaleString() : "unknown"]];
  $("task-fields").innerHTML = fields.map(([key, value]) => `<dt>${esc(key)}</dt><dd>${esc(value)}</dd>`).join("");

  const canJump = task.can_jump ?? isActiveTask(task);
  const canStop = task.can_stop ?? isActiveTask(task);
  $("task-terminal-note").hidden = canJump && canStop;
  $("task-terminal-note").textContent = provenance === "observed" ? "Observed tasks are inferred from the current tmux snapshot. Jump is revalidated; Stop is unavailable." : task.state_source === "workflow_registry" && isActiveTask(task) ? "This Workbench-managed workflow runs in tmux. Jump is ownership-verified; Stop is intentionally performed in the terminal pane." : "This task is terminal history. Jump and Stop are unavailable.";
  $("jump-task").disabled = !canJump;
  $("stop-task").disabled = !canStop;
  $("jump-task").title = canJump ? "Jump after revalidating the task location" : "This task location is unavailable";
  $("stop-task").title = canStop ? "Stop the Workbench-managed task" : "Workbench does not own this task";
}

document.querySelectorAll("[data-action]").forEach(button => {
  button.onclick = () => {
    const payload = { action: button.dataset.action, project_id: state.projectId };
    if (button.dataset.backend) payload.backend = button.dataset.backend;
    if (button.dataset.agent) payload.agent_kind = button.dataset.agent;
    action(payload);
  };
});

document.querySelectorAll("[data-task-action]").forEach(button => {
  button.onclick = () => {
    if (!state.taskId || button.disabled) return;
    if (button.dataset.taskAction === "stop_task" && !window.confirm(`Stop managed task ${state.taskId}?`)) return;
    action({ action: button.dataset.taskAction, task_id: state.taskId });
  };
});

$("clear-agent-history").onclick = () => {
  const projectTasks = (state.snapshot?.tasks || state.snapshot?.agents || []).filter(task => task.project_id === state.projectId && task.provenance !== "observed" && !isActiveTask(task));
  if (!state.projectId || projectTasks.length === 0) return;
  if (!window.confirm(`Clear ${projectTasks.length} terminal task records for ${state.projectId}? Active tasks will be preserved and the registry will be backed up.`)) return;
  state.taskId = null;
  action({ action: "clear_agent_history", project_id: state.projectId, task_ids: projectTasks.map(task => task.id) });
};

$("refresh").onclick = load;
$("doctor-details").onclick = () => {
  const failures = (state.snapshot?.doctor?.capabilities || []).filter(capability => capability.status !== "available").map(capability => `${capability.name}: ${capability.reason || capability.status}`).join("\n") || "All capabilities available";
  notice(failures);
};

load();
window.setInterval(load, 15000);
