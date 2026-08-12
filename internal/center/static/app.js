(() => {
  "use strict";

  const $ = (selector) => document.querySelector(selector);
  const esc = (value) => String(value ?? "").replace(/[&<>"']/g, (char) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  })[char]);
  const labels = {
    healthy: "健康", warning: "警告", critical: "严重", stale: "失联",
    open: "未处理", acknowledged: "已确认", resolved: "已解决",
    expected: "预期", approved: "已批准", temporary: "临时允许",
    unexpected: "待审核", denied: "禁止", covered: "已覆盖", missing: "缺失",
    confirmed: "已确认", rejected: "已否定", unreviewed: "未确认",
  };
  const state = {
    view: "overview", nodes: [], allNodes: [], overview: null, alerts: [], changes: [], databases: [],
    token: sessionStorage.getItem("fleet_token") || "",
    selectedAlerts: new Set(), selectedChanges: new Set(), topology: null, coverage: null, groups: [],
    activeGroup: sessionStorage.getItem("fleet_group") || "", topologyView: "all", lastSync: null,
    catalog: [], sources: null, agents: [], metricResult: null, rules: null, eventCatalog: null, eventSources: [], eventResult: null, eventRows: [], eventBefore: 0, eventPaged: false,
  };

  async function api(path, options = {}) {
    const headers = {
      ...(options.body ? { "Content-Type": "application/json" } : {}),
      ...(state.token ? { Authorization: `Bearer ${state.token}` } : {}),
      ...(options.headers || {}),
    };
    const response = await fetch(path, { cache: "no-store", ...options, headers });
    if (!response.ok) {
      let message = `请求失败 (${response.status})`;
      try { message = (await response.json()).error || message; } catch (_) { /* no JSON body */ }
      const error = new Error(message);
      error.status = response.status;
      throw error;
    }
    return response.status === 204 ? null : response.json();
  }

  function scoped(path, extra = {}) {
    const url = new URL(path, location.origin);
    if (state.activeGroup) url.searchParams.set("group", state.activeGroup);
    Object.entries(extra).forEach(([key, value]) => { if (value) url.searchParams.set(key, value); });
    return `${url.pathname}${url.search}`;
  }

  const fmt = (value) => value === undefined || value === null ? "-" : Number(value).toLocaleString("zh-CN", { maximumFractionDigits: 1 });
  const date = (value) => {
    const parsed = new Date(value);
    return Number.isNaN(parsed.valueOf()) ? "-" : parsed.toLocaleString("zh-CN", { hour12: false });
  };
  const healthReason = (value) => {
    const text = String(value || "");
    const stale = text.match(/^agent has not reported within (\d+) minutes?$/i);
    if (stale) return `Agent 已超过 ${stale[1]} 分钟未上报`;
    if (text === "healthy") return "采集正常";
    return text;
  };
  const status = (value) => `<span class="status ${esc(value)}">${esc(labels[value] || value || "未知")}</span>`;
  const severity = (value) => `<span class="severity ${esc((value || "info").toLowerCase())}">${esc((value || "info").toUpperCase())}</span>`;
  const empty = (message) => `<div class="empty">${esc(message)}</div>`;
  function toast(message, isError = false) {
    const target = $("#toast");
    target.textContent = message;
    target.classList.toggle("error", isError);
    target.classList.add("show");
    window.setTimeout(() => { target.classList.remove("show"); target.textContent = ""; }, 3200);
  }

  let dangerTimer = 0;
  function armDanger(button, confirmText) {
    if (button.dataset.confirm === "true") return true;
    window.clearTimeout(dangerTimer);
    document.querySelectorAll(".confirming").forEach((item) => {
      item.dataset.confirm = "false";
      item.classList.remove("confirming");
      item.textContent = item.dataset.originalText || item.textContent;
    });
    button.dataset.originalText = button.textContent;
    button.dataset.confirm = "true";
    button.classList.add("confirming");
    button.textContent = confirmText;
    dangerTimer = window.setTimeout(() => {
      if (!button.isConnected) return;
      button.dataset.confirm = "false";
      button.classList.remove("confirming");
      button.textContent = button.dataset.originalText;
    }, 3000);
    return false;
  }

  async function refresh() {
    if (document.hidden) return;
    const sync = $("#sync-state");
    sync.textContent = "正在同步";
    try {
      [state.groups, state.allNodes] = await Promise.all([api("/api/v1/groups"), api("/api/v1/nodes")]);
      if (state.activeGroup && !state.groups.some((group) => group.id === state.activeGroup)) {
        state.activeGroup = "";
        sessionStorage.removeItem("fleet_group");
      }
      renderGroups();
    } catch (error) {
      toast(`资源组读取失败：${error.message}`, true);
    }
    const viewKeys = {
      overview: ["overview"], nodes: ["nodes"], metrics: ["catalog"],
      sources: ["sources", "agents"], events: ["eventCatalog", "eventSources"],
      databases: ["databases"], alerts: ["alerts"], rules: ["rules", "catalog"],
      changes: ["changes"], coverage: ["coverage"], topology: [],
    };
    const selectedKeys = new Set(["overview", ...(viewKeys[state.view] || [])]);
    const specs = [
      ["overview", scoped("/api/v1/overview"), renderOverview, "#view-overview"],
      ["nodes", scoped("/api/v1/nodes"), renderNodes, "#nodes-table"],
      ["alerts", scoped("/api/v1/alerts"), renderAlerts, "#alerts-table"],
      ["changes", scoped("/api/v1/changes"), renderChanges, "#changes-table"],
      ["databases", scoped("/api/v1/databases"), renderDatabases, "#databases-table"],
      ["coverage", scoped("/api/v1/coverage"), renderCoverage, "#coverage-table"],
      ["catalog", scoped("/api/v1/telemetry/catalog"), renderMetricCatalog, "#metric-catalog"],
      ["sources", scoped("/api/v1/telemetry/sources"), renderSources, "#source-table"],
      ["agents", "/api/v1/agents", renderAgents, "#agent-table"],
      ["rules", "/api/v1/rules", renderRules, "#rules-table"],
      ["eventCatalog", scoped("/api/v1/events/catalog"), renderEventCatalog, "#event-metrics"],
      ["eventSources", scoped("/api/v1/events/sources"), renderEventFilters, "#events-table"],
    ].filter(([key]) => selectedKeys.has(key));
    const results = await Promise.allSettled(specs.map(([, path]) => api(path)));
    const failures = [];
    results.forEach((result, index) => {
      const [key, , render, targetSelector] = specs[index];
      const target = $(targetSelector);
      if (result.status === "fulfilled") {
        state[key] = result.value;
        render();
        target?.classList.remove("dataset-stale");
        return;
      }
      failures.push(`${key}: ${result.reason.message}`);
      target?.classList.add("dataset-stale");
      const hasPrevious = key === "overview" ? Boolean(state.overview) : Array.isArray(state[key]) ? state[key].length > 0 : Boolean(state[key]);
      if (!hasPrevious) {
        if (key === "overview") {
          $("#overview-metrics").classList.remove("loading");
          $("#overview-metrics").innerHTML = errorState(result.reason.message);
          $("#attention").classList.remove("loading");
          $("#attention").innerHTML = empty("暂无可用节点数据。");
        } else {
          target.classList.remove("loading");
          target.innerHTML = errorState(result.reason.message);
        }
      }
    });
    if (state.overview) {
      $("#alert-badge").textContent = state.overview.open_alerts || "";
      $("#change-badge").textContent = state.overview.pending_changes || "";
    }
    const banner = $("#data-banner");
    if (failures.length) {
      sync.textContent = failures.length === specs.length ? "同步失败" : "部分数据陈旧";
      sync.className = "error";
      banner.textContent = `有 ${failures.length} 个数据源刷新失败；页面保留上一次成功结果。${failures.join(" · ")}`;
      banner.classList.add("show");
      toast("部分运行数据刷新失败", true);
      if (results.some((result) => result.status === "rejected" && result.reason.status === 401) && !$("#token-dialog").open) {
        banner.textContent = "需要管理凭据才能读取 FleetScope 数据。凭据只保存在当前浏览器会话。";
        $("#token-input").value = state.token;
        $("#token-dialog").showModal();
      }
    } else {
      state.lastSync = new Date();
      sync.textContent = `已同步 ${state.lastSync.toLocaleTimeString("zh-CN", { hour12: false })}`;
      sync.className = "ok";
      banner.classList.remove("show");
    }
    if (state.view === "topology") await renderTopology();
    if (state.view === "metrics" && state.catalog.length) await queryMetric();
    if (state.view === "events" && !state.eventPaged) await queryEvents(true);
  }

  const errorState = (message) => `<div class="empty error-state"><b>数据暂不可用</b><span>${esc(message)}</span><button type="button" data-retry>重试</button></div>`;

  function renderGroups() {
    const select = $("#group-select");
    select.innerHTML = '<option value="">全部资源</option>' + state.groups.map((group) => `<option value="${esc(group.id)}">${esc(group.name)} · ${group.node_ids.length} 节点</option>`).join("");
    select.value = state.activeGroup;
    const active = state.groups.find((group) => group.id === state.activeGroup);
    $("#group-summary").textContent = active ? `${active.name} · ${active.node_ids.length} 节点${active.description ? ` · ${active.description}` : ""}` : `全部资源 · ${state.allNodes.length} 节点`;
    $("#group-list").innerHTML = state.groups.length ? state.groups.map((group) => `<article><div><b>${esc(group.name)}</b><span>${group.node_ids.length} 节点${group.description ? ` · ${esc(group.description)}` : ""}</span></div><div><button type="button" data-edit-group="${esc(group.id)}">编辑</button><button type="button" class="danger" data-delete-group="${esc(group.id)}">删除</button></div></article>`).join("") : empty("还没有资源组。全部视图当前显示所有节点。");
    $("#group-node-list").innerHTML = state.allNodes.length ? state.allNodes.map((node) => `<label><input type="checkbox" name="node" value="${esc(node.node_id)}"><span><b>${esc(node.hostname || node.node_id)}</b><small>${esc(node.node_id)}</small></span></label>`).join("") : empty("先让 Agent 上报节点，再创建资源组。");
  }

  function resetGroupForm(group = null) {
    $("#group-id").value = group?.id || "";
    $("#group-name").value = group?.name || "";
    $("#group-description").value = group?.description || "";
    const members = new Set(group?.node_ids || []);
    document.querySelectorAll('#group-node-list input[name="node"]').forEach((input) => { input.checked = members.has(input.value); });
  }

  function renderCoverage() {
    const data = state.coverage;
    if (!data) return;
    const percent = data.total ? Math.round(data.covered / data.total * 100) : 0;
    $("#coverage-metrics").classList.remove("loading");
    $("#coverage-metrics").innerHTML = `<div class="metric"><b>${percent}%</b><span>覆盖率</span><small>${data.covered}/${data.total} 个采集面</small></div><div class="metric"><b>${data.covered}</b><span>已覆盖</span><small>持续收到证据</small></div><div class="metric ${data.missing ? "warning" : ""}"><b>${data.missing}</b><span>监控空白</span><small>需要补充采集</small></div>`;
    const target = $("#coverage-table");
    target.classList.remove("loading");
    target.innerHTML = data.items.length ? `<table><thead><tr><th>节点</th><th>采集面</th><th>状态</th><th>证据</th><th>最近观察</th></tr></thead><tbody>${data.items.sort((a, b) => Number(a.covered) - Number(b.covered) || a.node_id.localeCompare(b.node_id)).map((item) => `<tr class="${item.covered ? "" : "coverage-missing"}"><td><button class="entity-link" data-node="${esc(item.node_id)}" type="button">${esc(item.node_id)}</button></td><td><span class="primary">${esc(item.name)}</span><span class="secondary">${esc(item.kind)}</span></td><td>${status(item.covered ? "covered" : "missing")}</td><td>${esc(item.detail)}</td><td>${date(item.observed_at)}</td></tr>`).join("")}</tbody></table>` : empty("这个资源组还没有可评估的节点。");
  }

  function usage(value) {
    const number = Number(value);
    if (!Number.isFinite(number)) return "-";
    const tone = number >= 90 ? "critical" : number >= 75 ? "warning" : "";
    return `<div class="usage ${tone}"><span>${fmt(number)}%</span><progress value="${Math.min(100, Math.max(0, number))}" max="100" aria-label="${fmt(number)}%"></progress></div>`;
  }

  function nodeTable(nodes) {
    return `<table><thead><tr><th>节点</th><th>状态</th><th>CPU</th><th>内存</th><th>磁盘</th><th>告警</th><th>最后上报</th></tr></thead><tbody>${nodes.map((node) => `<tr class="clickable" data-node="${esc(node.node_id)}" tabindex="0" role="button" aria-label="查看 ${esc(node.hostname || node.node_id)} 节点详情"><td><span class="primary">${esc(node.hostname || node.node_id)}</span><span class="secondary">${esc(node.node_id)}</span></td><td>${status(node.health)}<span class="secondary">${esc(healthReason(node.health_reason))}</span></td><td>${usage(node.metrics?.cpu_percent)}</td><td>${usage(node.metrics?.memory_percent)}</td><td>${usage(node.metrics?.disk_percent)}</td><td><b class="alert-count">${fmt(node.alert_count)}</b></td><td>${date(node.observed_at)}</td></tr>`).join("")}</tbody></table>`;
  }

  function renderOverview() {
    const data = state.overview;
    if (!data) return;
    const items = [
      [data.total_nodes, "节点", ""], [data.critical_nodes, "严重节点", "critical"],
      [data.warning_nodes, "警告节点", "warning"], [data.stale_nodes, "失联节点", ""],
      [data.open_alerts, "未处理告警", data.critical_alerts ? "critical" : ""],
      [data.pending_changes, "待审核变化", data.pending_changes ? "warning" : ""],
    ];
    $("#overview-metrics").classList.remove("loading");
    renderStartPanel(data);
    const targets = ["nodes", "nodes", "nodes", "nodes", "alerts", "changes"];
    $("#overview-metrics").innerHTML = items.map(([value, name, className], index) => `<button type="button" data-go-view="${targets[index]}" class="metric ${className}"><b>${fmt(value)}</b><span>${name}</span><small>查看详情</small></button>`).join("");
    $("#overview-time").textContent = `数据生成于 ${date(data.generated_at)}`;
    $("#attention").classList.remove("loading");
    $("#attention").innerHTML = data.needs_attention.length ? nodeTable(data.needs_attention) : empty("当前没有需要立即处理的节点。");
    const resourceNames = { hosts: "主机", processes: "进程", endpoints: "监听端口", services: "服务", containers: "容器", networks: "网络", volumes: "卷" };
    $("#resource-totals").innerHTML = Object.entries(data.resource_totals || {}).map(([key, value]) => `<div class="resource-item"><b>${fmt(value)}</b><span>${esc(resourceNames[key] || key)}</span></div>`).join("") || empty("尚无资源数据");
  }

  function renderStartPanel(data) {
    const panel = $("#fleet-start");
    let content = "";
    if (!data.total_nodes && state.activeGroup) {
      content = `<span class="start-panel-mark" aria-hidden="true">0</span><div><strong>当前资源组没有节点</strong><p>把节点加入资源组，或切回全部资源继续查看。</p></div><button type="button" data-open-groups>管理资源组</button>`;
    } else if (!data.total_nodes) {
      content = `<span class="start-panel-mark" aria-hidden="true">1</span><div><strong>接入第一台机器</strong><p>原生 Agent 会直接上报主机指标、事件和 InfraScout 资产证据，不依赖外部采集器。</p></div><div class="start-panel-actions"><button type="button" data-go-view="sources">打开数据接入</button><button type="button" class="quiet" data-open-token>写入凭据</button></div>`;
    } else if (data.stale_nodes === data.total_nodes) {
      content = `<span class="start-panel-mark warning" aria-hidden="true">!</span><div><strong>所有 Agent 已停止上报</strong><p>当前统计来自历史数据。先恢复 Agent 或接入链路，再据此处理告警与变化。</p></div><div class="start-panel-actions"><button type="button" data-go-view="sources">检查数据接入</button><button type="button" class="quiet" data-retry>重新同步</button></div>`;
    }
    panel.innerHTML = content;
    panel.classList.toggle("hidden", !content);
  }

  function renderNodes() {
    const query = $("#node-filter").value.trim().toLowerCase();
    const rows = state.nodes.filter((node) => {
      const searchable = [node.node_id, node.hostname, ...Object.entries(node.labels || {}).flat()].join(" ").toLowerCase();
      return !query || searchable.includes(query);
    });
    const target = $("#nodes-table");
    target.classList.remove("loading");
    target.innerHTML = rows.length ? nodeTable(rows) : empty(query ? "没有符合筛选条件的节点。" : "尚无节点，请在被监控机器运行 fleetctl agent。");
    renderMetricNodeOptions();
  }

  function renderMetricNodeOptions() {
    const select = $("#metric-node");
    const current = select.value;
	const nodeNames = new Map(state.nodes.map((node) => [node.node_id, node.hostname || node.node_id]));
	(state.catalog || []).flatMap((item) => item.nodes || []).forEach((nodeID) => { if (!nodeNames.has(nodeID)) nodeNames.set(nodeID, nodeID); });
	const nodes = [...nodeNames.entries()].sort((a, b) => a[1].localeCompare(b[1], "zh-CN"));
    select.innerHTML = '<option value="">全部节点</option>' + nodes.map(([nodeID, name]) => `<option value="${esc(nodeID)}">${esc(name)}</option>`).join("");
    if ([...select.options].some((option) => option.value === current)) select.value = current;
  }

  function renderMetricCatalog() {
    const catalog = state.catalog || [];
    const metricSelect = $("#metric-name");
    const selected = metricSelect.value;
    metricSelect.innerHTML = '<option value="">选择指标</option>' + catalog.map((item) => `<option value="${esc(item.metric)}">${esc(item.metric)}</option>`).join("");
	const mostUseful = [...catalog].sort((a, b) => Number(b.samples || 0) - Number(a.samples || 0))[0];
    metricSelect.value = catalog.some((item) => item.metric === selected) ? selected : (mostUseful?.metric || "");
    const series = catalog.reduce((total, item) => total + Number(item.series || 0), 0);
    const samples = catalog.reduce((total, item) => total + Number(item.samples || 0), 0);
    const freshest = catalog.reduce((latest, item) => Math.max(latest, Number(item.last_seen_ms || 0)), 0);
    $("#metric-summary").classList.remove("loading");
    $("#metric-summary").innerHTML = `<div class="metric"><b>${fmt(catalog.length)}</b><span>指标名称</span><small>当前存储目录</small></div><div class="metric"><b>${fmt(series)}</b><span>活跃序列</span><small>节点、来源与标签组合</small></div><div class="metric"><b>${fmt(samples)}</b><span>保留样本</span><small>查询窗口内可用</small></div>`;
    const target = $("#metric-catalog");
    target.classList.remove("loading");
    target.innerHTML = catalog.length ? `<table><thead><tr><th>指标</th><th>类型 / 单位</th><th>序列</th><th>样本</th><th>节点</th><th>来源</th><th>最后采样</th></tr></thead><tbody>${catalog.map((item) => `<tr class="clickable" data-metric="${esc(item.metric)}" tabindex="0"><td><span class="primary mono">${esc(item.metric)}</span></td><td>${esc(item.kind)}${item.unit ? ` · ${esc(item.unit)}` : ""}</td><td>${fmt(item.series)}</td><td>${fmt(item.samples)}</td><td>${esc((item.nodes || []).join("、"))}</td><td>${esc((item.sources || []).join("、"))}</td><td>${date(item.last_seen_ms)}</td></tr>`).join("")}</tbody></table>` : empty("时序存储中还没有指标。启动原生 Agent 或向兼容入口发送数据后会自动出现。");
    if (freshest) $("#metric-query-meta").textContent = `最新样本 ${date(freshest)}`;
	renderMetricNodeOptions();
  }

  async function queryMetric() {
    const metric = $("#metric-name").value;
    if (!metric) {
      $("#metric-chart").classList.remove("loading");
      $("#metric-chart").innerHTML = empty("先从指标目录中选择一个指标。");
      return;
    }
    const rangeHours = { "15m": .25, "1h": 1, "6h": 6, "24h": 24, "168h": 168 }[$("#metric-range").value] || 1;
    const step = rangeHours <= .25 ? "10s" : rangeHours <= 1 ? "30s" : rangeHours <= 6 ? "2m" : rangeHours <= 24 ? "10m" : "1h";
    const end = Date.now(), start = end - rangeHours * 60 * 60 * 1000;
    const params = new URLSearchParams({ metric, start: String(Math.floor(start)), end: String(end), step, aggregate: $("#metric-aggregate").value });
    if ($("#metric-node").value) params.set("node", $("#metric-node").value);
    const chart = $("#metric-chart");
    chart.classList.add("loading");
    try {
      if (state.activeGroup) params.set("group", state.activeGroup);
      state.metricResult = await api(`/api/v1/telemetry/query?${params}`);
      renderMetricResult(metric);
    } catch (error) {
      chart.classList.remove("loading");
      chart.innerHTML = errorState(error.message);
      $("#metric-series").innerHTML = empty("查询失败，序列明细不可用。");
    }
  }

  function renderMetricResult(metric) {
    const result = state.metricResult;
    const series = result?.series || [];
    const chart = $("#metric-chart");
    chart.classList.remove("loading");
    $("#metric-query-meta").textContent = `${series.length} 条序列 · ${date(result.start_ms)} 至 ${date(result.end_ms)} · 步长 ${Math.round(result.step_ms / 1000)} 秒`;
    if (!series.length) {
      chart.innerHTML = empty("所选节点和时间范围内没有样本。");
      $("#metric-series").innerHTML = empty("没有序列明细。");
      return;
    }
    chart.innerHTML = metricChart(metric, result);
    $("#metric-series").innerHTML = `<table><thead><tr><th>节点 / 来源</th><th>标签</th><th>样本</th><th>最后值</th><th>最小</th><th>最大</th><th>最后采样</th></tr></thead><tbody>${series.map((item) => {
      const values = item.points.map((point) => Number(point.value)).filter(Number.isFinite);
      const last = item.points.at(-1);
      const labelsText = Object.entries(item.labels || {}).map(([key, value]) => `${key}=${value}`).join(" · ");
      return `<tr><td><span class="primary">${esc(item.node_id)}</span><span class="secondary">${esc(item.source)}</span></td><td class="mono">${esc(labelsText || "-")}</td><td>${fmt(values.length)}</td><td>${fmt(last?.value)} ${esc(item.unit || "")}</td><td>${fmt(Math.min(...values))}</td><td>${fmt(Math.max(...values))}</td><td>${date(last?.timestamp_ms)}</td></tr>`;
    }).join("")}</tbody></table>`;
  }

  function metricChart(metric, result) {
    const width = 1080, height = 330, left = 64, right = 24, top = 28, bottom = 48;
    const values = result.series.flatMap((item) => item.points.map((point) => Number(point.value))).filter(Number.isFinite);
    let min = Math.min(...values), max = Math.max(...values);
    if (min === max) { min -= Math.max(1, Math.abs(min) * .1); max += Math.max(1, Math.abs(max) * .1); }
    const padding = (max - min) * .08; min -= padding; max += padding;
    const x = (time) => left + (Number(time) - result.start_ms) / Math.max(1, result.end_ms - result.start_ms) * (width - left - right);
    const y = (value) => top + (max - Number(value)) / Math.max(1e-9, max - min) * (height - top - bottom);
    const grid = Array.from({ length: 5 }, (_, index) => {
      const ratio = index / 4, yPos = top + ratio * (height - top - bottom), value = max - ratio * (max - min);
      return `<line x1="${left}" y1="${yPos}" x2="${width-right}" y2="${yPos}" class="chart-grid"></line><text x="${left-10}" y="${yPos+4}" text-anchor="end" class="chart-axis">${esc(fmt(value))}</text>`;
    }).join("");
    const xTicks = Array.from({ length: 6 }, (_, index) => {
      const ratio = index / 5, value = result.start_ms + ratio * (result.end_ms - result.start_ms), xPos = left + ratio * (width - left - right);
      const label = new Date(value).toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false });
      return `<line x1="${xPos}" y1="${top}" x2="${xPos}" y2="${height-bottom}" class="chart-grid chart-grid-x"></line><text x="${xPos}" y="${height-16}" text-anchor="${index === 0 ? "start" : index === 5 ? "end" : "middle"}" class="chart-axis">${esc(label)}</text>`;
    }).join("");
    const colors = ["#12634f", "#3d6ea8", "#b06a24", "#9b3f54", "#6d5a9b", "#527a3a", "#287d8e", "#8a5d2e", "#4b62a3", "#a14d3d"];
    const lines = result.series.map((item, index) => {
      const points = item.points.map((point) => `${x(point.timestamp_ms).toFixed(1)},${y(point.value).toFixed(1)}`).join(" ");
      return `<polyline points="${points}" class="chart-line" style="stroke:${colors[index % colors.length]}"><title>${esc(item.node_id)} · ${esc(item.source)}</title></polyline>`;
    }).join("");
    const legend = result.series.map((item, index) => `<span><i style="background:${colors[index % colors.length]}"></i><b>${esc(item.node_id)}</b><small>${esc(item.source)}${Object.keys(item.labels || {}).length ? ` · ${esc(Object.values(item.labels).join("/"))}` : ""}</small></span>`).join("");
    return `<div class="chart-head"><div><strong class="mono">${esc(metric)}</strong><span>${esc(result.series[0]?.unit || "无单位")}</span></div><div class="chart-legend">${legend}</div></div><svg viewBox="0 0 ${width} ${height}" role="img" aria-label="${esc(metric)} 指标趋势">${grid}${xTicks}<line x1="${left}" y1="${height-bottom}" x2="${width-right}" y2="${height-bottom}" class="chart-axis-line"></line>${lines}</svg>`;
  }

  function renderSources() {
    const data = state.sources;
    if (!data) return;
    const allowedNodes = new Set(state.nodes.map((node) => node.node_id));
    const sources = (data.sources || []).filter((source) => !state.activeGroup || allowedNodes.has(source.node_id));
    const adapters = data.adapters || [];
    $("#source-adapters").classList.remove("loading");
    $("#source-adapters").innerHTML = adapters.map((adapter) => `<article class="adapter ${adapter.status === "ready" ? "ready" : "planned"}"><div><span class="adapter-status">${adapter.status === "ready" ? "可用" : "未就绪"}</span><strong>${esc(adapter.id)}</strong></div><p>${adapter.id === "native" ? "自研 Agent 批次协议，支持持久队列与序列去重。" : `${esc(adapter.format || "兼容格式")} 直接转换到原生时序存储。`}</p><code>${esc(adapter.path || "-")}</code></article>`).join("");
    $("#source-summary").textContent = `${sources.length} 个节点/来源组合`;
    const target = $("#source-table");
    target.classList.remove("loading");
    target.innerHTML = sources.length ? `<table><thead><tr><th>节点</th><th>来源</th><th>状态</th><th>序列数</th><th>最后序列</th><th>最后接收</th></tr></thead><tbody>${sources.map((source) => {
      const age = Date.now() - new Date(source.last_seen_at).valueOf();
      const health = age < 2 * 60 * 1000 ? "healthy" : age < 10 * 60 * 1000 ? "warning" : "stale";
      return `<tr><td class="primary">${esc(source.node_id)}</td><td><span class="source-kind">${esc(source.source)}</span></td><td>${status(health)}</td><td>${fmt(source.series)}</td><td>${source.last_sequence ? fmt(source.last_sequence) : "-"}</td><td>${date(source.last_seen_at)}</td></tr>`;
    }).join("")}</tbody></table>` : empty("当前资源组还没有收到遥测数据。");
    $("#source-endpoints").innerHTML = adapters.map((adapter) => `<div><code>POST ${esc(adapter.path || "-")}</code><span>${adapter.id === "native" ? "Content-Type: application/json" : adapter.id === "otlp" ? "OTLP/HTTP JSON · X-Node-ID" : `${esc(adapter.format || "text")} · X-Node-ID`}</span></div>`).join("");
  }

  function renderAgents() {
    const allowedNodes = new Set(state.nodes.map((node) => node.node_id));
    const agents = (state.agents || []).filter((item) => !state.activeGroup || allowedNodes.has(item.node_id));
    const active = agents.filter((item) => !item.revoked_at).length;
    $("#agent-summary").textContent = `${active} 个有效身份 · ${agents.length - active} 个已吊销`;
    const target = $("#agent-table");
    target.classList.remove("loading");
    target.innerHTML = agents.length ? `<table><thead><tr><th>节点</th><th>身份状态</th><th>最近活动</th><th>登记时间</th><th>操作</th></tr></thead><tbody>${agents.map((item) => {
      const age = item.last_seen_at ? Date.now() - new Date(item.last_seen_at).valueOf() : Infinity;
      const health = item.revoked_at ? "stale" : age < 2 * 60 * 1000 ? "healthy" : age < 10 * 60 * 1000 ? "warning" : "stale";
      const label = item.revoked_at ? "已吊销" : item.last_seen_at ? labels[health] : "等待首次上报";
      return `<tr class="${item.revoked_at ? "agent-revoked" : ""}"><td><button class="entity-link" data-node="${esc(item.node_id)}" type="button">${esc(item.node_id)}</button></td><td><span class="status ${health}">${esc(label)}</span></td><td>${item.last_seen_at ? date(item.last_seen_at) : "-"}</td><td>${date(item.created_at)}</td><td>${item.revoked_at ? `<span class="secondary">${date(item.revoked_at)} 吊销</span>` : `<button type="button" class="danger" data-agent-revoke="${esc(item.node_id)}">吊销身份</button>`}</td></tr>`;
    }).join("")}</tbody></table>` : empty("还没有 Agent 身份。节点首次使用管理令牌登记后会显示在这里。");
  }

  function renderRules() {
    const payload = state.rules || { rules: [], states: {} };
    const rules = payload.rules || [];
    const enabled = rules.filter((rule) => rule.enabled).length;
    const firing = Object.values(payload.states || {}).reduce((total, counts) => total + Number(counts.firing || 0), 0);
    const pending = Object.values(payload.states || {}).reduce((total, counts) => total + Number(counts.pending || 0), 0);
    $("#rule-metrics").classList.remove("loading");
    $("#rule-metrics").innerHTML = `<div class="metric"><b>${fmt(enabled)}</b><span>启用规则</span><small>${rules.length} 条规则定义</small></div><div class="metric ${firing ? "critical" : ""}"><b>${fmt(firing)}</b><span>触发序列</span><small>正在产生告警</small></div><div class="metric ${pending ? "warning" : ""}"><b>${fmt(pending)}</b><span>等待持续</span><small>尚未达到 for 时间</small></div>`;
    const operators = { gt: ">", gte: "≥", lt: "<", lte: "≤", eq: "=", neq: "≠" };
    const target = $("#rules-table");
    target.classList.remove("loading");
    target.innerHTML = rules.length ? `<table><thead><tr><th>状态</th><th>规则 / 指标</th><th>条件</th><th>范围</th><th>触发状态</th><th>更新时间</th><th>操作</th></tr></thead><tbody>${rules.map((rule) => {
      const counts = payload.states?.[rule.id] || {};
      const scope = [rule.node_id ? `节点 ${rule.node_id}` : "全部节点", rule.source ? `来源 ${rule.source}` : "全部来源", ...Object.entries(rule.labels || {}).map(([key, value]) => `${key}=${value}`)].join(" · ");
      return `<tr><td><span class="status ${rule.enabled ? "healthy" : "stale"}">${rule.enabled ? "启用" : "停用"}</span></td><td><span class="primary">${esc(rule.name)}</span><span class="secondary mono">${esc(rule.metric)}</span></td><td>${severity(rule.severity)} <span class="mono">${esc(operators[rule.operator] || rule.operator)} ${fmt(rule.threshold)}</span><span class="secondary">持续 ${rule.for_seconds ? `${fmt(rule.for_seconds)} 秒` : "立即"}</span></td><td>${esc(scope)}</td><td><span class="rule-count ${counts.firing ? "firing" : ""}">${fmt(counts.firing || 0)} 触发</span><span class="secondary">${fmt(counts.pending || 0)} 等待</span></td><td>${date(rule.updated_at)}</td><td><div class="row-actions"><button type="button" data-rule-edit="${esc(rule.id)}">编辑</button><button type="button" data-rule-toggle="${esc(rule.id)}">${rule.enabled ? "停用" : "启用"}</button><button type="button" class="danger" data-rule-delete="${esc(rule.id)}">删除</button></div></td></tr>`;
    }).join("")}</tbody></table>` : empty("还没有告警规则。创建规则后，每条匹配序列会独立计算持续时间和恢复状态。");
  }

  function parseRuleLabels(raw) {
    const labels = {};
    for (const item of raw.split(",").map((value) => value.trim()).filter(Boolean)) {
      const index = item.indexOf("=");
      if (index <= 0) throw new Error("标签选择器必须使用 key=value，并用逗号分隔");
      labels[item.slice(0, index).trim()] = item.slice(index + 1).trim();
    }
    return labels;
  }

  function openRule(rule = null) {
    $("#rule-title").textContent = rule ? "编辑告警规则" : "新建告警规则";
    $("#rule-id").value = rule?.id || "";
    $("#rule-name").value = rule?.name || "";
    $("#rule-description").value = rule?.description || "";
    $("#rule-metric").innerHTML = (state.catalog || []).map((item) => `<option value="${esc(item.metric)}">${esc(item.metric)}</option>`).join("") || '<option value="">暂无指标</option>';
    $("#rule-metric").value = rule?.metric || state.catalog?.[0]?.metric || "";
    $("#rule-operator").value = rule?.operator || "gt";
    $("#rule-threshold").value = rule?.threshold ?? "";
    $("#rule-for").value = rule?.for_seconds ?? 0;
    $("#rule-severity").value = rule?.severity || "warning";
    $("#rule-enabled").value = String(rule?.enabled ?? true);
    $("#rule-node").value = rule?.node_id || "";
    $("#rule-source").value = rule?.source || "";
    $("#rule-labels").value = Object.entries(rule?.labels || {}).map(([key, value]) => `${key}=${value}`).join(",");
    $("#rule-dialog").showModal();
    $("#rule-name").focus();
  }

  async function saveRule(event) {
    event.preventDefault();
    const id = $("#rule-id").value;
    let labels;
    try { labels = parseRuleLabels($("#rule-labels").value); } catch (error) { toast(error.message, true); return; }
    const body = {
      name: $("#rule-name").value.trim(), description: $("#rule-description").value.trim(), metric: $("#rule-metric").value,
      node_id: $("#rule-node").value.trim(), source: $("#rule-source").value.trim(), labels,
      operator: $("#rule-operator").value, threshold: Number($("#rule-threshold").value), for_seconds: Number($("#rule-for").value),
      severity: $("#rule-severity").value, enabled: $("#rule-enabled").value === "true",
    };
    $("#rule-submit").disabled = true;
    try {
      await api(id ? `/api/v1/rules/${encodeURIComponent(id)}` : "/api/v1/rules", { method: id ? "PATCH" : "POST", body: JSON.stringify(body) });
      $("#rule-dialog").close();
      toast(id ? "告警规则已更新" : "告警规则已创建");
      await refresh();
    } catch (error) {
      if (error.message.includes("unauthorized")) $("#token-dialog").showModal();
      toast(error.message, true);
    } finally { $("#rule-submit").disabled = false; }
  }

  function renderEventCatalog() {
    const catalog = state.eventCatalog || { total: 0, by_severity: {}, by_kind: {}, by_source: {} };
    const failures = Number(catalog.by_severity?.error || 0) + Number(catalog.by_severity?.critical || 0);
    $("#event-metrics").classList.remove("loading");
    $("#event-metrics").innerHTML = `<div class="metric"><b>${fmt(catalog.total)}</b><span>保留事件</span><small>${Object.keys(catalog.by_kind || {}).length} 种类型</small></div><div class="metric ${failures ? "critical" : ""}"><b>${fmt(failures)}</b><span>错误与严重</span><small>当前保留窗口</small></div><div class="metric"><b>${fmt(Object.keys(catalog.by_source || {}).length)}</b><span>事件来源</span><small>${catalog.last_ms ? `最新 ${date(catalog.last_ms)}` : "等待数据"}</small></div>`;
    renderEventFilters();
  }

  function renderEventFilters() {
    const preserveOptions = (selector, values, allLabel) => {
      const select = $(selector), current = select.value;
      select.innerHTML = `<option value="">${allLabel}</option>` + values.map((value) => `<option value="${esc(value)}">${esc(value)}</option>`).join("");
      if (values.includes(current)) select.value = current;
    };
    preserveOptions("#event-node", [...new Set((state.eventSources || []).map((source) => source.node_id))].sort(), "全部节点");
    preserveOptions("#event-source", Object.keys(state.eventCatalog?.by_source || {}).sort(), "全部来源");
    preserveOptions("#event-kind", Object.keys(state.eventCatalog?.by_kind || {}).sort(), "全部类型");
  }

  async function queryEvents(reset = true) {
    if (reset) {
      state.eventRows = [];
      state.eventBefore = 0;
      state.eventPaged = false;
    } else {
      state.eventPaged = true;
    }
    const hours = Number($("#event-range").value.replace("h", "")) || 24;
    const end = Date.now(), start = end - hours * 60 * 60 * 1000;
    const params = new URLSearchParams({ start: String(start), end: String(end), limit: "200" });
    if (state.activeGroup) params.set("group", state.activeGroup);
    const filters = { node: "#event-node", source: "#event-source", kind: "#event-kind", severity: "#event-severity", search: "#event-search" };
    Object.entries(filters).forEach(([key, selector]) => { if ($(selector).value.trim()) params.set(key, $(selector).value.trim()); });
    if (!reset && state.eventBefore) params.set("before", String(state.eventBefore));
    const target = $("#events-table");
    target.classList.add("loading");
    try {
      const result = await api(`/api/v1/events?${params}`);
      state.eventResult = result;
      state.eventRows.push(...(result.events || []));
      state.eventBefore = result.next_before_ms || 0;
      renderEvents();
    } catch (error) {
      target.classList.remove("loading");
      if (!state.eventRows.length) target.innerHTML = errorState(error.message);
      toast(`事件查询失败：${error.message}`, true);
    }
  }

  function renderEvents() {
    const target = $("#events-table");
    target.classList.remove("loading");
    const rows = state.eventRows;
    $("#event-result-meta").textContent = `${rows.length} 条已加载${state.eventResult?.total_matched > rows.length ? ` · 当前页查询匹配 ${fmt(state.eventResult.total_matched)}` : ""}`;
    target.innerHTML = rows.length ? rows.map((event) => {
      const attributes = Object.entries(event.attributes || {});
      return `<article class="event-row severity-${esc(event.severity)}"><time>${date(event.timestamp_ms)}</time><div class="event-level">${severity(event.severity)}</div><div class="event-content"><div><strong>${esc(event.service || event.kind)}</strong><span class="source-kind">${esc(event.node_id)} · ${esc(event.source)}</span></div><p>${esc(event.message)}</p>${attributes.length ? `<details><summary>${attributes.length} 个属性</summary><dl>${attributes.map(([key, value]) => `<dt>${esc(key)}</dt><dd>${esc(value)}</dd>`).join("")}</dl></details>` : ""}</div><span class="event-kind">${esc(event.kind)}</span></article>`;
    }).join("") : empty("所选时间和筛选条件下没有事件。启动 Agent 文件日志采集后，事件会在这里出现。");
    $("#event-load-more").classList.toggle("hidden", !state.eventBefore);
  }

  function clearEventFilters() {
    $("#event-search").value = "";
    $("#event-node").value = "";
    $("#event-severity").value = "";
    $("#event-kind").value = "";
    $("#event-source").value = "";
    $("#event-range").value = "24h";
    queryEvents(true);
  }

  function renderDatabases() {
    const target = $("#databases-table");
    target.classList.remove("loading");
    target.innerHTML = state.databases.length ? `<table><thead><tr><th>数据库</th><th>引擎</th><th>节点</th><th>状态</th><th>版本</th><th>连接数</th><th>采集方式</th><th>观察时间</th></tr></thead><tbody>${state.databases.map((item) => `<tr><td class="primary">${esc(item.name)}</td><td>${esc(item.engine)}</td><td>${esc(item.node_id)}</td><td>${status(item.status === "ok" ? "healthy" : item.status === "warning" ? "warning" : "critical")}</td><td>${esc(item.version || "-")}</td><td>${fmt(item.connections)}</td><td>${item.latency_ms ? `探针 · ${fmt(item.latency_ms)} ms` : "原生统计"}</td><td>${date(item.observed_at)}</td></tr>`).join("")}</tbody></table>` : empty("尚无数据库数据。请在 Agent applications 或 probes 配置中添加 PostgreSQL/MySQL 目标。");
  }

  function renderAlerts() {
    const filter = $("#alert-filter").value;
    const level = $("#alert-severity").value;
    const rows = state.alerts.filter((alert) => (!filter || alert.status === filter) && (!level || alert.severity.toLowerCase() === level));
    const target = $("#alerts-table");
    const openEvidence = new Set([...target.querySelectorAll("details[data-alert-evidence][open]")].map((details) => details.dataset.alertEvidence));
    target.classList.remove("loading");
    const visible = new Set(rows.map((alert) => alert.id));
    [...state.selectedAlerts].forEach((id) => { if (!visible.has(id)) state.selectedAlerts.delete(id); });
    target.innerHTML = rows.length ? `<table><thead><tr><th><span class="sr-only">选择</span></th><th>级别</th><th>告警与证据</th><th>节点</th><th>状态与处理</th><th>更新时间</th><th>操作</th></tr></thead><tbody>${rows.map((alert) => {
      const evidence = alert.evidence || {}, metric = evidence.metric || "";
      const evidenceRows = Object.entries(evidence).filter(([key]) => key !== "metric").map(([key, value]) => `<dt>${esc(key)}</dt><dd>${esc(typeof value === "object" ? JSON.stringify(value) : value)}</dd>`).join("");
      return `<tr class="alert-row severity-${esc(alert.severity)}"><td><input class="row-check" type="checkbox" data-alert-select="${esc(alert.id)}" aria-label="选择 ${esc(alert.title)}" ${state.selectedAlerts.has(alert.id) ? "checked" : ""}></td><td>${severity(alert.severity)}</td><td><span class="primary">${esc(alert.title)}</span><span class="secondary">${esc(alert.detail)}</span>${alert.value !== undefined || evidenceRows ? `<details class="alert-evidence" data-alert-evidence="${esc(alert.id)}"><summary>判断依据</summary><div class="alert-threshold"><b>${fmt(alert.value)}</b><span>当前值</span><b>${fmt(alert.threshold)}</b><span>阈值</span></div>${evidenceRows ? `<dl>${evidenceRows}</dl>` : ""}</details>` : ""}</td><td><button class="entity-link" data-node="${esc(alert.node_id)}" type="button">${esc(alert.node_id)}</button></td><td>${status(alert.status)}${alert.assignee ? `<span class="secondary">${esc(alert.assignee)}${alert.note ? ` · ${esc(alert.note)}` : ""}</span>` : ""}</td><td>${date(alert.updated_at || alert.observed_at)}</td><td><div class="row-actions">${metric ? `<button data-alert-metric="${esc(metric)}" data-alert-node="${esc(alert.node_id)}" type="button">查指标</button>` : ""}${alert.status === "open" ? `<button data-alert="${esc(alert.id)}" data-status="acknowledged" type="button">确认</button>` : ""}${alert.status !== "resolved" ? `<button data-alert="${esc(alert.id)}" data-status="resolved" type="button">解决</button>` : `<button data-alert="${esc(alert.id)}" data-status="open" type="button">重新打开</button>`}</div></td></tr>`;
    }).join("")}</tbody></table>` : empty("当前筛选条件下没有告警。");
    target.querySelectorAll("details[data-alert-evidence]").forEach((details) => { details.open = openEvidence.has(details.dataset.alertEvidence); });
    updateBulkBar();
  }

  function updateBulkBar() {
    const count = state.selectedAlerts.size;
    $("#alert-bulk").querySelector("span").textContent = count ? `已选择 ${count} 条告警` : "选择告警后可批量确认";
    $("#bulk-ack").disabled = count === 0;
  }

  function renderChanges() {
    const filter = $("#change-filter").value;
    const rows = state.changes.filter((change) => !filter || change.classification === filter);
    const target = $("#changes-table");
    target.classList.remove("loading");
    const visible = new Set(rows.map((change) => change.id));
    [...state.selectedChanges].forEach((id) => { if (!visible.has(id)) state.selectedChanges.delete(id); });
    target.innerHTML = rows.length ? `<table><thead><tr><th><span class="sr-only">选择</span></th><th>风险</th><th>变化</th><th>节点</th><th>分类与审核</th><th>观察时间</th><th>处理</th></tr></thead><tbody>${rows.map((change) => `<tr><td><input class="row-check" type="checkbox" data-change-select="${esc(change.id)}" aria-label="选择 ${esc(change.summary || change.resource_id)}" ${state.selectedChanges.has(change.id) ? "checked" : ""}></td><td>${severity(change.severity)}</td><td><span class="primary">${esc(change.summary || change.resource_id)}</span><span class="secondary">${esc(change.kind)} · ${esc(change.resource_id)}</span></td><td><button class="entity-link" data-node="${esc(change.node_id)}" type="button">${esc(change.node_id)}</button></td><td>${status(change.classification)}${change.release_id ? `<span class="secondary">发布 ${esc(change.release_id)}</span>` : ""}${change.reviewed_by ? `<span class="secondary">${esc(change.reviewed_by)}${change.decision_note ? ` · ${esc(change.decision_note)}` : ""}</span>` : ""}</td><td>${date(change.observed_at)}</td><td><select data-change="${esc(change.id)}" aria-label="变化分类"><option value="">选择操作</option><option value="expected">标记预期</option><option value="approved">批准</option><option value="temporary">临时允许</option><option value="denied">禁止</option><option value="unexpected">退回待审核</option></select></td></tr>`).join("")}</tbody></table>` : empty("当前筛选条件下没有变化。");
    updateChangeBulkBar();
  }

  function updateChangeBulkBar() {
    const count = state.selectedChanges.size;
    $("#change-bulk").querySelector("span").textContent = count ? `已选择 ${count} 条变化` : "选择变化后可批量审核";
    $("#bulk-change-expected").disabled = count === 0;
    $("#bulk-change-approved").disabled = count === 0;
  }

  function sparkline(history, key, title) {
    const points = history.map((row) => Number(row.metrics?.[key])).filter(Number.isFinite);
    if (!points.length) return `<div class="trend"><strong>${esc(title)}</strong>${empty("暂无样本")}</div>`;
    const width = 300, height = 86, max = Math.max(100, ...points), min = Math.min(0, ...points);
    const coords = points.map((value, index) => {
      const x = points.length === 1 ? width / 2 : index * width / (points.length - 1);
      const y = height - 8 - (value - min) / Math.max(1, max - min) * (height - 16);
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    }).join(" ");
    return `<div class="trend"><div><strong>${esc(title)}</strong><span>${fmt(points.at(-1))}% · ${points.length} 个样本</span></div><svg viewBox="0 0 ${width} ${height}" role="img" aria-label="${esc(title)}历史趋势"><path d="M0 ${height - 8} H${width}" class="grid-line"></path><polyline points="${coords}" class="trend-line"></polyline></svg></div>`;
  }

  async function openNode(id) {
    const dialog = $("#node-dialog");
    dialog.showModal();
    $("#node-title").textContent = id;
    $("#node-subtitle").textContent = "正在读取节点数据";
    $("#node-detail").innerHTML = empty("正在加载");
    try {
      const [detail, history] = await Promise.all([
        api(`/api/v1/nodes/${encodeURIComponent(id)}`),
        api(`/api/v1/nodes/${encodeURIComponent(id)}/history`),
      ]);
      const node = detail.summary, metrics = node.metrics || {}, agentInfo = detail.report?.agent || {};
      $("#node-title").textContent = node.hostname || node.node_id;
      $("#node-subtitle").textContent = `${node.node_id} · ${labels[node.health] || node.health} · ${date(node.observed_at)}`;
      const capacity = [["CPU", metrics.cpu_percent], ["内存", metrics.memory_percent], ["磁盘", metrics.disk_percent]];
      $("#node-detail").innerHTML = `<div class="agent-facts"><span><b>${esc(agentInfo.version || "-")}</b>Agent 版本</span><span><b>${esc(agentInfo.os || "-")} / ${esc(agentInfo.arch || "-")}</b>运行平台</span><span><b>${esc(agentInfo.hostname || node.hostname || "-")}</b>Agent 主机名</span></div><div class="detail-metrics">${capacity.map(([name, value]) => `<div class="detail-metric"><b>${fmt(value)}%</b><span>${name}</span><progress value="${Number(value) || 0}" max="100">${fmt(value)}%</progress></div>`).join("")}<div class="detail-metric"><b>${fmt(metrics.load_1)}</b><span>1 分钟负载</span></div><div class="detail-metric"><b>${fmt(metrics.docker_running)}/${fmt(metrics.docker_containers)}</b><span>运行容器</span></div><div class="detail-metric"><b>${fmt(node.alert_count)}</b><span>未处理告警</span></div></div><h3>历史趋势</h3><div class="history-grid">${sparkline(history, "cpu_percent", "CPU")}${sparkline(history, "memory_percent", "内存")}${sparkline(history, "disk_percent", "磁盘")}</div><h3>资源清单</h3>${Object.entries(node.summary || {}).length ? `<div class="resource-strip">${Object.entries(node.summary).map(([key, value]) => `<div class="resource-item"><b>${fmt(value)}</b><span>${esc(key)}</span></div>`).join("")}</div>` : empty("没有资源摘要")}<h3>最近变化</h3>${detail.changes.length ? `<div class="table-wrap"><table><tbody>${detail.changes.slice(0, 8).map((change) => `<tr><td>${severity(change.severity)}</td><td>${esc(change.summary || change.resource_id)}</td><td>${esc(labels[change.classification] || change.classification)}</td></tr>`).join("")}</tbody></table></div>` : empty("没有记录到变化")}`;
    } catch (error) {
      $("#node-detail").innerHTML = empty(error.message);
    }
  }

  function openAction(kind, ids, value, context) {
    const isChange = kind === "change";
    const isRelationship = kind === "relationship";
    $("#action-kind").value = kind;
    $("#action-id").value = Array.isArray(ids) ? ids.join(",") : ids;
    $("#action-value").value = value;
    $("#action-title").textContent = isRelationship ? `${labels[value] || value}拓扑关系` : isChange ? `审核变化：${labels[value] || value}` : `${labels[value] || value}告警`;
    $("#action-context").textContent = context;
    $("#release-field").classList.toggle("hidden", !(isChange && value === "expected"));
    $("#expiry-field").classList.toggle("hidden", !(isChange && value === "temporary"));
    $("#action-release").value = "";
    $("#action-expiry").value = "";
    $("#action-note").value = "";
    $("#action-submit").textContent = isRelationship ? "保存关系确认" : isChange ? "保存审核结论" : "保存告警处置";
    $("#action-dialog").showModal();
    $("#action-actor").focus();
  }

  async function updateAlert(ids, value, assignee, note) {
    try {
      await Promise.all(ids.map((id) => api(`/api/v1/alerts/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ status: value, assignee, note }) })));
      state.selectedAlerts.clear();
      toast(ids.length > 1 ? `${ids.length} 条告警已确认` : "告警处置已保存");
      await refresh();
    } catch (error) {
      if (error.message.includes("unauthorized")) $("#token-dialog").showModal();
      toast(error.message, true);
    }
  }

  async function updateChange(ids, classification, reviewedBy, decisionNote, releaseID, expiresAt) {
    const body = { classification, reviewed_by: reviewedBy, decision_note: decisionNote, release_id: releaseID || "" };
    if (classification === "temporary") body.expires_at = expiresAt;
    try {
      await Promise.all(ids.map((id) => api(`/api/v1/changes/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify(body) })));
      state.selectedChanges.clear();
      toast(ids.length > 1 ? `${ids.length} 条变化已完成审核` : "变化审核结论已保存"); await refresh();
    } catch (error) {
      if (error.message.includes("unauthorized")) $("#token-dialog").showModal();
      toast(error.message, true);
    }
  }

  async function updateRelationship(id, confirmation, reviewedBy, note) {
    try {
      await api(`/api/v1/topology/relationships/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ confirmation, reviewed_by: reviewedBy, note }) });
      toast("拓扑关系确认已保存");
      await renderTopology();
    } catch (error) {
      if (error.message.includes("unauthorized")) $("#token-dialog").showModal();
      toast(error.message, true);
    }
  }

  async function renderTopology() {
    const target = $("#topology");
    target.classList.add("loading");
    try {
      const topology = await api(scoped("/api/v1/topology", { view: state.topologyView }));
      state.topology = topology;
      target.classList.remove("loading");
      const typeCounts = topology.nodes.reduce((counts, node) => ({ ...counts, [node.type]: (counts[node.type] || 0) + 1 }), {});
      const reviewed = topology.edges.filter((edge) => edge.confirmation !== "unreviewed").length;
      $("#topology-stats").innerHTML = `<span>${topology.nodes.length} 个资源</span><span>${topology.edges.length} 条关系</span><span>${reviewed}/${topology.edges.length} 已审核</span>${Object.entries(typeCounts).map(([type, count]) => `<span>${esc(type)} ${count}</span>`).join("")}<em>${date(topology.generated_at)}</em>`;
      renderRelationships(topology);
      if (!topology.nodes.length) { target.innerHTML = empty("当前资源组或拓扑视角没有可显示的数据。"); return; }
      const order = ["host", "service", "process", "route", "endpoint"];
      const grouped = topology.nodes.reduce((groups, node) => { (groups[node.type] ||= []).push(node); return groups; }, {});
      const types = Object.keys(grouped).sort((a, b) => (order.indexOf(a) < 0 ? 99 : order.indexOf(a)) - (order.indexOf(b) < 0 ? 99 : order.indexOf(b)));
      const cellWidth = 220, cellHeight = 78, boxWidth = 174, boxHeight = 52;
      const width = Math.max(720, types.length * cellWidth + 40), height = Math.max(360, Math.max(...types.map((type) => grouped[type].length)) * cellHeight + 70);
      const positions = {};
      types.forEach((type, column) => grouped[type].forEach((node, row) => { positions[node.id] = { x: 25 + column * cellWidth, y: 50 + row * cellHeight }; }));
      const edges = topology.edges.filter((edge) => positions[edge.source] && positions[edge.target]).map((edge) => {
        const from = positions[edge.source], to = positions[edge.target];
        const confidence = Number(edge.confidence || 0);
        return `<line x1="${from.x + boxWidth / 2}" y1="${from.y + boxHeight / 2}" x2="${to.x + boxWidth / 2}" y2="${to.y + boxHeight / 2}" class="topology-edge ${confidence < .8 ? "uncertain" : ""} ${esc(edge.confirmation)}" marker-end="url(#arrow)"><title>${esc(edge.type)} · 置信度 ${fmt(confidence * 100)}% · ${esc(labels[edge.confirmation] || edge.confirmation)} · ${esc((edge.evidence || []).join("、"))}</title></line>`;
      }).join("");
      const nodes = topology.nodes.map((node) => {
        const point = positions[node.id], className = ["host", "service", "endpoint"].includes(node.type) ? node.type : "resource";
        return `<g class="topology-node ${className}" transform="translate(${point.x} ${point.y})"><rect width="${boxWidth}" height="${boxHeight}" rx="4"></rect><text x="12" y="21" class="node-label">${esc(node.label).slice(0, 24)}</text><text x="12" y="39" class="node-meta">${esc(node.type)} · ${esc(node.node_id)}</text><title>${esc(node.id)}</title></g>`;
      }).join("");
      const headings = types.map((type, index) => `<text x="${25 + index * cellWidth}" y="25" class="layer-label">${esc(type.toUpperCase())} · ${grouped[type].length}</text>`).join("");
      const mobileList = types.map((type) => `<section><h3>${esc(type)} <span>${grouped[type].length}</span></h3>${grouped[type].map((node) => `<div><b>${esc(node.label)}</b><small>${esc(node.node_id)}${node.health ? ` · ${esc(labels[node.health] || node.health)}` : ""}</small></div>`).join("")}</section>`).join("");
      target.innerHTML = `<svg class="topology-svg" viewBox="0 0 ${width} ${height}" width="${width}" height="${height}" role="img" aria-label="分层资源拓扑"><defs><marker id="arrow" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="5" markerHeight="5" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z"></path></marker></defs>${headings}${edges}${nodes}</svg><div class="topology-list">${mobileList}</div>`;
    } catch (error) {
      target.classList.remove("loading"); target.innerHTML = errorState(error.message); $("#relationship-list").innerHTML = empty("关系证据暂不可用。");
    }
  }

  function renderRelationships(topology) {
    const nodeByID = new Map(topology.nodes.map((node) => [node.id, node]));
    const target = $("#relationship-list");
    target.innerHTML = topology.edges.length ? topology.edges.map((edge) => {
      const source = nodeByID.get(edge.source), destination = nodeByID.get(edge.target);
      const context = `${source?.label || edge.source} → ${destination?.label || edge.target}`;
      return `<article class="relationship ${esc(edge.confirmation)}"><div><span class="primary">${esc(context)}</span><span class="secondary">${esc(edge.type)} · 置信度 ${fmt(Number(edge.confidence || 0) * 100)}% · ${esc((edge.evidence || []).join("、") || "无来源说明")}</span><span class="secondary">首次 ${date(edge.first_seen_at)} · 最近 ${date(edge.last_seen_at)}${edge.reviewed_by ? ` · ${esc(edge.reviewed_by)}：${esc(edge.review_note || "已审核")}` : ""}</span></div><div>${status(edge.confirmation)}<button type="button" data-relationship="${esc(edge.id)}" data-confirmation="confirmed" data-context="${esc(context)}">确认</button><button type="button" data-relationship="${esc(edge.id)}" data-confirmation="rejected" data-context="${esc(context)}">否定</button></div></article>`;
    }).join("") : empty("当前视角没有资源关系。");
  }

  document.addEventListener("click", async (event) => {
    const navigation = event.target.closest("nav button[data-view]");
    if (navigation) {
      state.view = navigation.dataset.view;
      document.querySelectorAll("nav button").forEach((item) => {
        item.classList.toggle("active", item === navigation);
        if (item === navigation) item.setAttribute("aria-current", "page"); else item.removeAttribute("aria-current");
      });
      document.querySelectorAll(".view").forEach((item) => item.classList.toggle("active", item.id === `view-${state.view}`));
      await refresh();
      const heading = document.querySelector(".view.active h1");
      if (heading) { heading.tabIndex = -1; heading.focus({ preventScroll: true }); }
      return;
    }
    const go = event.target.closest("[data-go-view]");
    if (go) {
      document.querySelector(`nav button[data-view="${go.dataset.goView}"]`)?.click();
      return;
    }
    if (event.target.closest("[data-retry]")) { refresh(); return; }
    if (event.target.closest("[data-open-token]")) { $("#token-input").value = state.token; $("#token-dialog").showModal(); return; }
    if (event.target.closest("[data-open-groups]")) { resetGroupForm(); $("#group-dialog").showModal(); return; }
    const metricRow = event.target.closest("[data-metric]");
    if (metricRow) {
      $("#metric-name").value = metricRow.dataset.metric;
      document.querySelector('nav button[data-view="metrics"]')?.click();
      return;
    }
    const alertMetric = event.target.closest("[data-alert-metric]");
    if (alertMetric) {
      $("#metric-name").value = alertMetric.dataset.alertMetric;
      $("#metric-node").value = alertMetric.dataset.alertNode;
      document.querySelector('nav button[data-view="metrics"]')?.click();
      return;
    }
    const editRule = event.target.closest("[data-rule-edit]");
    if (editRule) {
      openRule((state.rules?.rules || []).find((rule) => rule.id === editRule.dataset.ruleEdit));
      return;
    }
    const toggleRule = event.target.closest("[data-rule-toggle]");
    if (toggleRule) {
      const rule = (state.rules?.rules || []).find((item) => item.id === toggleRule.dataset.ruleToggle);
      if (!rule) return;
      if (rule.enabled && !armDanger(toggleRule, "再次点击停用")) return;
      try {
        await api(`/api/v1/rules/${encodeURIComponent(rule.id)}`, { method: "PATCH", body: JSON.stringify({ ...rule, enabled: !rule.enabled }) });
        toast(rule.enabled ? "告警规则已停用" : "告警规则已启用");
        await refresh();
      } catch (error) {
        if (error.message.includes("unauthorized")) $("#token-dialog").showModal();
        toast(error.message, true);
      }
      return;
    }
    const deleteRule = event.target.closest("[data-rule-delete]");
    if (deleteRule) {
      if (!armDanger(deleteRule, "再次点击删除")) return;
      try {
        await api(`/api/v1/rules/${encodeURIComponent(deleteRule.dataset.ruleDelete)}`, { method: "DELETE" });
        toast("告警规则已删除");
        await refresh();
      } catch (error) {
        if (error.message.includes("unauthorized")) $("#token-dialog").showModal();
        toast(error.message, true);
      }
      return;
    }
    const revokeAgent = event.target.closest("[data-agent-revoke]");
    if (revokeAgent) {
      if (!armDanger(revokeAgent, "再次点击吊销")) return;
      try {
        await api(`/api/v1/agents/${encodeURIComponent(revokeAgent.dataset.agentRevoke)}`, { method: "DELETE" });
        toast("Agent 身份已吊销；节点需要显式重新登记才能继续上报");
        await refresh();
      } catch (error) {
        if (error.status === 401) $("#token-dialog").showModal();
        toast(error.message, true);
      }
      return;
    }
    const relationshipButton = event.target.closest("[data-relationship]");
    if (relationshipButton) {
      openAction("relationship", relationshipButton.dataset.relationship, relationshipButton.dataset.confirmation, relationshipButton.dataset.context);
      return;
    }
    const editGroup = event.target.closest("[data-edit-group]");
    if (editGroup) {
      resetGroupForm(state.groups.find((group) => group.id === editGroup.dataset.editGroup));
      return;
    }
    const deleteGroup = event.target.closest("[data-delete-group]");
    if (deleteGroup) {
      if (!armDanger(deleteGroup, "再次点击删除")) return;
      try {
        await api(`/api/v1/groups/${encodeURIComponent(deleteGroup.dataset.deleteGroup)}`, { method: "DELETE" });
        if (state.activeGroup === deleteGroup.dataset.deleteGroup) {
          state.activeGroup = "";
          sessionStorage.removeItem("fleet_group");
        }
        resetGroupForm();
        toast("资源组已删除");
        await refresh();
      } catch (error) {
        if (error.message.includes("unauthorized")) $("#token-dialog").showModal();
        toast(error.message, true);
      }
      return;
    }
    const row = event.target.closest("[data-node]"); if (row) openNode(row.dataset.node);
    const alertButton = event.target.closest("button[data-alert]");
    if (alertButton) {
      const alert = state.alerts.find((item) => item.id === alertButton.dataset.alert);
      openAction("alert", alertButton.dataset.alert, alertButton.dataset.status, alert ? `${alert.title} · ${alert.node_id}` : alertButton.dataset.alert);
    }
    if (event.target.matches("[data-close]")) event.target.closest("dialog").close();
  });
  document.addEventListener("change", (event) => {
    if (event.target.matches("select[data-change]") && event.target.value) {
      const change = state.changes.find((item) => item.id === event.target.dataset.change);
      openAction("change", event.target.dataset.change, event.target.value, change ? `${change.summary || change.resource_id} · ${change.node_id}` : event.target.dataset.change);
      event.target.value = "";
    }
    if (event.target.id === "group-select") {
      state.activeGroup = event.target.value;
      state.eventPaged = false;
      state.eventRows = [];
      state.eventBefore = 0;
      if (state.activeGroup) sessionStorage.setItem("fleet_group", state.activeGroup); else sessionStorage.removeItem("fleet_group");
      refresh();
    }
    if (event.target.id === "topology-view") {
      state.topologyView = event.target.value;
      renderTopology();
    }
    if (event.target.matches("[data-alert-select]")) {
      if (event.target.checked) state.selectedAlerts.add(event.target.dataset.alertSelect);
      else state.selectedAlerts.delete(event.target.dataset.alertSelect);
      updateBulkBar();
    }
    if (event.target.matches("[data-change-select]")) {
      if (event.target.checked) state.selectedChanges.add(event.target.dataset.changeSelect);
      else state.selectedChanges.delete(event.target.dataset.changeSelect);
      updateChangeBulkBar();
    }
  });
  document.addEventListener("keydown", (event) => {
    const row = event.target.closest("[data-node]");
    if (row && (event.key === "Enter" || event.key === " ")) {
      event.preventDefault();
      openNode(row.dataset.node);
    }
    const metricRow = event.target.closest("[data-metric]");
    if (metricRow && (event.key === "Enter" || event.key === " ")) {
      event.preventDefault();
      metricRow.click();
    }
  });
  $("#refresh").addEventListener("click", refresh);
  $("#metric-refresh").addEventListener("click", queryMetric);
  $("#metric-query").addEventListener("submit", (event) => { event.preventDefault(); queryMetric(); });
  $("#rule-create").addEventListener("click", () => openRule());
  $("#rule-form").addEventListener("submit", saveRule);
  $("#event-refresh").addEventListener("click", () => queryEvents(true));
  $("#event-query").addEventListener("submit", (event) => { event.preventDefault(); queryEvents(true); });
  $("#event-load-more").addEventListener("click", () => queryEvents(false));
  $("#event-clear").addEventListener("click", clearEventFilters);
  $("#node-filter").addEventListener("input", renderNodes);
  $("#alert-filter").addEventListener("change", renderAlerts);
  $("#alert-severity").addEventListener("change", renderAlerts);
  $("#change-filter").addEventListener("change", renderChanges);
  $("#bulk-ack").addEventListener("click", () => openAction("alert", [...state.selectedAlerts], "acknowledged", `${state.selectedAlerts.size} 条已选择告警`));
  $("#bulk-change-expected").addEventListener("click", () => openAction("change", [...state.selectedChanges], "expected", `${state.selectedChanges.size} 条已选择变化`));
  $("#bulk-change-approved").addEventListener("click", () => openAction("change", [...state.selectedChanges], "approved", `${state.selectedChanges.size} 条已选择变化`));
  $("#action-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const kind = $("#action-kind").value;
    const ids = $("#action-id").value.split(",").filter(Boolean);
    const value = $("#action-value").value;
    const actor = $("#action-actor").value.trim();
    const note = $("#action-note").value.trim();
    if (!actor || !note) return;
    if (kind === "change" && value === "temporary" && !$("#action-expiry").value) { toast("请选择临时允许的截止时间", true); return; }
    $("#action-submit").disabled = true;
    try {
      $("#action-dialog").close();
      if (kind === "alert") await updateAlert(ids, value, actor, note);
      else if (kind === "change") await updateChange(ids, value, actor, note, $("#action-release").value.trim(), $("#action-expiry").value ? new Date($("#action-expiry").value).toISOString() : "");
      else await updateRelationship(ids[0], value, actor, note);
    } finally { $("#action-submit").disabled = false; }
  });
  $("#group-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const id = $("#group-id").value;
    const body = { name: $("#group-name").value.trim(), description: $("#group-description").value.trim(), node_ids: [...document.querySelectorAll('#group-node-list input[name="node"]:checked')].map((input) => input.value) };
    try {
      await api(id ? `/api/v1/groups/${encodeURIComponent(id)}` : "/api/v1/groups", { method: id ? "PATCH" : "POST", body: JSON.stringify(body) });
      resetGroupForm();
      toast(id ? "资源组已更新" : "资源组已创建");
      await refresh();
    } catch (error) {
      if (error.message.includes("unauthorized")) $("#token-dialog").showModal();
      toast(error.message, true);
    }
  });
  $("#group-manage").addEventListener("click", () => { resetGroupForm(); $("#group-dialog").showModal(); });
  $("#group-reset").addEventListener("click", () => resetGroupForm());
  $("#token-button").addEventListener("click", () => { $("#token-input").value = state.token; $("#token-dialog").showModal(); });
  $("#save-token").addEventListener("click", () => { state.token = $("#token-input").value.trim(); sessionStorage.setItem("fleet_token", state.token); toast("管理凭据已保存到当前会话"); window.setTimeout(refresh, 0); });
  refresh();
  function userIsChoosingAction() {
    return Boolean(document.querySelector("dialog[open]") || document.activeElement?.matches("select[data-change]"));
  }
  window.setInterval(function () { if (!document.hidden && !userIsChoosingAction()) refresh(); }, 30000);
  document.addEventListener("visibilitychange", function () { if (!document.hidden && !userIsChoosingAction()) refresh(); });
})();
