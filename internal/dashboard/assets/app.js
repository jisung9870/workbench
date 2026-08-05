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
  return task.lifecycle === "active";
}

function taskCard(task) {
  return `<button class="agent-card ${task.id === state.taskId ? "selected" : ""}" data-task="${esc(task.id)}"><span class="agent-head"><span class="avatar">${esc(task.agent_kind[0])}</span><span class="status ${esc(task.state)}">${esc(task.state)}</span></span><strong>${esc(task.agent_kind)}</strong><small>${esc(task.backend)} · ${esc(task.id)}</small></button>`;
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
  $("platform").textContent = data.platform;
  $("profile").textContent = data.profile;
  $("project-count").textContent = data.projects.length;

  const missing = data.doctor.summary.unavailable_core;
  $("health-title").textContent = missing ? `${missing} core checks missing` : "Core ready";
  $("health-detail").textContent = `${data.doctor.summary.available} available · ${data.doctor.summary.unavailable_optional} optional missing`;
  document.querySelector(".pulse").classList.toggle("ok", !missing);

  $("projects").innerHTML = data.projects.map(project => {
    const activeCount = data.agents.filter(task => task.project_id === project.id && isActiveTask(task)).length;
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
  const projectTasks = data.agents.filter(task => task.project_id === project.id);
  const activeTasks = projectTasks.filter(isActiveTask);
  const historyTasks = projectTasks.filter(task => !isActiveTask(task));
  const worktrees = data.worktrees.filter(item => item.project_id === project.id);
  const changes = data.changes.find(item => item.project_id === project.id) || { changed: 0, branch: "unknown", changed_files: [] };

  $("metric-agents").textContent = activeTasks.length;
  $("active-agent-count").textContent = activeTasks.length;
  $("metric-worktrees").textContent = worktrees.length;
  $("metric-changes").textContent = changes.changed;
  $("metric-branch").textContent = changes.branch || "branch unavailable";

  $("agents").innerHTML = activeTasks.length ? activeTasks.map(taskCard).join("") : '<div class="row"><span>No active Agent tasks</span></div>';
  $("history-count").textContent = historyTasks.length;
  $("agent-registry-path").textContent = data.agent_registry_path || "agents.json";
  $("agent-history-list").innerHTML = historyTasks.length ? historyTasks.map(taskCard).join("") : '<div class="row"><span>No terminal task history</span></div>';
  $("clear-agent-history").disabled = historyTasks.length === 0;
  bindTaskCards();

  $("worktrees").innerHTML = worktrees.length ? worktrees.map(item => `<div class="row"><strong title="${esc(item.path)}">${esc(item.branch || item.id)}</strong><span>${item.dirty ? "dirty" : "clean"}${item.managed ? " · managed" : ""}</span></div>`).join("") : '<div class="row"><span>No linked worktrees</span></div>';
  $("changes").innerHTML = changes.unavailable ? `<div class="row"><span>${esc(changes.unavailable)}</span></div>` : changes.changed_files.length ? changes.changed_files.slice(0, 8).map(file => `<div class="row"><strong title="${esc(file)}">${esc(file)}</strong><span>changed</span></div>`).join("") : '<div class="row"><span>Working tree clean</span></div>';
  const capabilities = data.doctor.capabilities.filter(capability => capability.scope === "core" || capability.status === "unavailable").slice(0, 8);
  $("doctor").innerHTML = capabilities.map(capability => `<div class="row"><strong>${esc(capability.name)}</strong><span>${esc(capability.status)}</span></div>`).join("");
  renderTask();
}

function renderTask() {
  const task = state.snapshot?.agents.find(item => item.id === state.taskId);
  if (!task) state.taskId = null;
  $("task-empty").hidden = Boolean(task);
  $("task-detail").hidden = !task;
  if (!task) return;

  $("task-kind").textContent = task.agent_kind[0];
  $("task-id").textContent = task.id;
  $("task-state").textContent = task.state;
  $("task-state").className = `status ${task.state}`;
  const fields = [["Agent", task.agent_kind], ["Backend", task.backend], ["Project", task.project_id], ["Worktree", task.worktree_id || "main"], ["CWD", task.cwd], ["Updated", new Date(task.last_event_at).toLocaleString()]];
  $("task-fields").innerHTML = fields.map(([key, value]) => `<dt>${esc(key)}</dt><dd>${esc(value)}</dd>`).join("");

  const active = isActiveTask(task);
  $("task-terminal-note").hidden = active;
  $("jump-task").disabled = !active;
  $("stop-task").disabled = !active;
  $("jump-task").title = active ? "Jump to the registered backend" : "Terminal task history cannot be opened";
  $("stop-task").title = active ? "Stop the registered task" : "This task is already terminal";
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
    if (button.dataset.taskAction === "stop_agent" && !window.confirm(`Stop registered task ${state.taskId}?`)) return;
    action({ action: button.dataset.taskAction, task_id: state.taskId });
  };
});

$("clear-agent-history").onclick = () => {
  const projectTasks = state.snapshot?.agents.filter(task => task.project_id === state.projectId && !isActiveTask(task)) || [];
  if (!state.projectId || projectTasks.length === 0) return;
  if (!window.confirm(`Clear ${projectTasks.length} terminal task records for ${state.projectId}? Active tasks will be preserved and the registry will be backed up.`)) return;
  state.taskId = null;
  action({ action: "clear_agent_history", project_id: state.projectId, task_ids: projectTasks.map(task => task.id) });
};

$("refresh").onclick = load;
$("doctor-details").onclick = () => {
  const failures = state.snapshot.doctor.capabilities.filter(capability => capability.status !== "available").map(capability => `${capability.name}: ${capability.reason || capability.status}`).join("\n") || "All capabilities available";
  notice(failures);
};

load();
window.setInterval(load, 15000);
