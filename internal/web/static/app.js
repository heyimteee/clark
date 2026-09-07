(function () {
  "use strict";

  const SESSION_KEY = "clark.session";
  const TOAST_MS = 2400;

  let token = sessionStorage.getItem(SESSION_KEY) || null;
  let state = null;
  let mode = "bento";
  let chatWs = null;
  let logsWs = null;
  let chatBackoff = 1000;
  let logsBackoff = 1000;
  let logsOpen = false;
  let logsPaused = false;
  let logsPinned = false;
  let historyScope = "web";
  let historyVip = "";
  let historyAll = false;
  let historyLoading = false;
  let vipSort = "default";
  let voiceOn = localStorage.getItem("clark-voiceOn") === "true";
  let chatSessionId = parseInt(localStorage.getItem("clark.chatSession") || "0", 10) || 0;
  let chatSessions = [];
  let recording = false;
  let mediaRecorder = null;
  let audioCtx = null;
  let analyser = null;
  let vadRAF = 0;
  let micStream = null;
  let chunks = [];
  let silenceStart = 0;
  let recStartAt = 0;
  let wakeRecognition = null;
  let wakeHeld = false;
  let chatBusy = false;
  // Speech playback: speechGen increments on every speakTTS() so stale chains
  // (a reply superseded by a newer one, or voice-off) are dropped; speechSource
  // is the actively playing BufferSource so it can be cut off mid-word.
  let speechGen = 0;
  let speechSource = null;
  let spokenCount = 0;
  let spokenUpTo = 0; // char offset up to which streamed text has been queued for TTS
  let disarming = false; // true while disarmVoice triggered stopRecording -> onRecordingStop
  let clarkSpeaking = false; // true while Clark is playing audio (TTS/affirmation/processing)
  let activeClips = new Set(); // HTMLAudioElements for affirmations/processing
  let playChain = Promise.resolve(); // serial playback, fetches concurrent

  const $ = function (sel, root) {
    return (root || document).querySelector(sel);
  };
  const el = function (html) {
    const t = document.createElement("template");
    t.innerHTML = html.trim();
    return t.content.firstElementChild;
  };
  const esc = function (s) {
    return String(s == null ? "" : s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  };

  /* ---------------- api ---------------- */

  async function api(path, opts) {
    opts = opts || {};
    const headers = Object.assign({}, opts.headers || {});
    if (token) headers["Authorization"] = "Bearer " + token;
    const res = await fetch(path, Object.assign({}, opts, { headers }));
    if (res.status === 401) {
      logout();
      throw new Error("session expired");
    }
    let data = null;
    try { data = await res.json(); } catch (e) { /* empty body */ }
    if (!res.ok) {
      throw new Error((data && data.error) || ("request failed (" + res.status + ")"));
    }
    if (data && data.state) state = data.state;
    return data;
  }

  function captureState() {
    if (!state) return;
    const elStatus = $("#cfg-status");
    const elThinking = $("#cfg-thinking");
    const elLimit = $("#cfg-limit");
    const elCtx = $("#cfg-ctx");
    if (elStatus) elStatus.checked = state.enabled;
    if (elThinking) elThinking.checked = state.thinking;
    if (elLimit) elLimit.value = state.historyLimit;
    if (elCtx && document.activeElement !== elCtx) elCtx.value = state.context;
  }

  function renderState() {
    // Deliberately does NOT touch the chat transcript — server state pushes
    // fire on any setting change (any tab/device), and reseeding here would
    // wipe a live conversation. The greeting is seeded once at boot.
    const tag = $("#env-tag") || $(".env-tag");
    if (tag && state && state.version) {
      const ver = String(state.version).split("-")[0]; // tag only, drop describe suffix
      tag.textContent = (ver.charAt(0) === "v" ? ver : "v" + ver) + " console";
    }
    captureState();
    renderVoiceMeta();
    renderVips();
    renderAccess();
  }

  /* ---------------- toast ---------------- */

  function toast(msg, kind) {
    let t = $("#toast");
    if (!t) {
      t = el('<div id="toast" role="status"></div>');
      document.body.appendChild(t);
    }
    t.textContent = msg;
    t.classList.remove("ok", "err");
    if (kind) t.classList.add(kind);
    t.classList.add("show");
    clearTimeout(t._timer);
    t._timer = setTimeout(function () { t.classList.remove("show"); }, TOAST_MS);
  }

  function toastOk(msg) { toast(msg, "ok"); }
  function toastErr(msg) { toast(msg, "err"); }

  /* ---------------- login ---------------- */

  function showLogin() {
    $("#app").innerHTML = "";
    const v = el(
      '<div id="login">' +
        '<div class="wordmark">clark</div>' +
        '<p class="tagline">voice console for the house</p>' +
        '<div class="card">' +
        '<label class="field"><span class="lbl">access key</span>' +
        '<input id="login-key" class="input" type="password" placeholder="&#183;&#183;&#183;&#183;&#183;&#183;&#183;&#183;" autocomplete="current-password" autofocus></label>' +
        '<div class="form-row"><button id="login-btn" class="btn primary">unlock</button></div>' +
        '<p class="err hidden"></p>' +
        "</div>" +
        "</div>"
    );
    $("#app").appendChild(v);

    const key = $("#login-key");
    const btn = $("#login-btn");
    const err = $(".err", v);

    async function attempt() {
      if (!key.value) return;
      btn.disabled = true;
      err.classList.add("hidden");
      try {
        const r = await fetch("/web/api/login", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ key: key.value }),
        });
        const data = await r.json().catch(function () { return {}; });
        if (!r.ok || !data.token) {
          throw new Error(data.error || "wrong key");
        }
        token = data.token;
        sessionStorage.setItem(SESSION_KEY, token);
        boot();
      } catch (e) {
        err.textContent = e.message;
        err.classList.remove("hidden");
        btn.disabled = false;
        key.select();
      }
    }
    btn.addEventListener("click", attempt);
    key.addEventListener("keydown", function (e) { if (e.key === "Enter") attempt(); });
  }

  function logout() {
    // Revoke the session server-side first so the token is dead even if it
    // was copied elsewhere; local cleanup happens regardless of the result.
    if (token) {
      fetch("/web/api/logout", { method: "POST", headers: { "Authorization": "Bearer " + token } })
        .catch(function () { /* best effort */ });
    }
    token = null;
    sessionStorage.removeItem(SESSION_KEY);
    if (chatWs) { chatWs.close(); chatWs = null; }
    if (logsWs) { logsWs.close(); logsWs = null; }
    showLogin();
  }

  /* ---------------- boot ---------------- */

  async function boot() {
    $("#app").innerHTML = "";
    const shell = el(
      '<div id="shell" class="hidden">' +
        '<header id="header"><div class="container">' +
          '<div class="brand"><span class="wordmark">clark</span><span class="env-tag" id="env-tag">clark console</span></div>' +
          '<div class="spacer"></div>' +
          '<div id="live" title="live link"><span class="dot"></span>live</div>' +
          '<div class="mode-switch">' +
            '<button id="mode-bento" class="active" aria-pressed="true">bento</button>' +
            '<button id="mode-chat" aria-pressed="false">chat</button>' +
            '<button id="mode-kanban" aria-pressed="false">kanban</button>' +
            '<button id="mode-protocols" aria-pressed="false">protocols</button>' +
          "</div>" +
          '<button id="btn-logout" class="btn">lock</button>' +
        "</div></header>" +
        '<main class="container" id="main">' +
          '<section id="bento">' +
            '<div class="card tile-config"><h2>Config</h2><p class="sub">runtime settings</p>' +
              '<div class="toggle-row"><div><div class="t-lbl">clark status</div><div class="t-desc">responds to messages</div></div>' +
                '<label class="switch"><input type="checkbox" id="cfg-status"><span class="track"></span><span class="knob"></span></label></div>' +
              '<div class="toggle-row"><div><div class="t-lbl">thinking</div><div class="t-desc">show reasoning steps</div></div>' +
                '<label class="switch"><input type="checkbox" id="cfg-thinking"><span class="track"></span><span class="knob"></span></label></div>' +
              '<div class="toggle-row"><div><div class="t-lbl">history limit</div><div class="t-desc">turns remembered per chat</div></div>' +
                '<input id="cfg-limit" class="input" type="number" min="1" max="30"></div>' +
              '<form id="config-form">' +
                '<label class="field"><span class="lbl">context</span>' +
                '<textarea id="cfg-ctx" class="input" rows="3"></textarea></label>' +
                '<div class="ctx-save-row"><button class="btn" type="submit">save context</button></div>' +
              "</form>" +
            "</div>" +
            '<div class="card tile-voice"><h2>Voice</h2><p class="sub">speech seam</p>' +
              '<div class="voice-meta">' +
                '<div class="row"><span class="k">stt</span><span class="v" id="voice-stt"></span></div>' +
                '<div class="row"><span class="k">tts</span><span class="v" id="voice-tts"></span></div>' +
                '<div class="row"><span class="k">voice</span><span class="v" id="voice-voice"></span></div>' +
              "</div>" +
              '<div class="toggle-row"><div><div class="t-lbl">voice on</div><div class="t-desc">wake word + hands-free talk</div></div>' +
                '<label class="switch"><input type="checkbox" id="voice-toggle"><span class="track"></span><span class="knob"></span></label></div>' +
              '<div class="toggle-row"><div><div class="t-lbl">voice alerts</div><div class="t-desc">speak alerts aloud (voice) \u2014 or stay silent and buzz the phone instead</div></div>' +
                '<label class="switch"><input type="checkbox" id="alert-mode-toggle"><span class="track"></span><span class="knob"></span></label></div>' +
              '<div class="voice-actions" id="voice-actions">' +
                '<button class="btn" id="btn-testtts">test voice</button>' +
              "</div>" +
              '<div id="voice-status">voice off \u2014 flip the toggle</div>' +
            "</div>" +
            '<div class="card tile-access"><h2>Access</h2><p class="sub">tools per contact</p>' +
              '<select id="vip-picker" class="input"></select>' +
              '<div id="access-list"></div>' +
              '<div id="access-pager"></div>' +
            "</div>" +
            '<div class="card tile-vips"><h2>VIPs</h2><p class="sub">people who reach clark</p>' +
              '<div class="vip-head-row">' +
                '<div class="sort-tabs" id="vip-sort">' +
                  '<button data-sort="default" class="active">default</button>' +
                  '<button data-sort="az">A→Z</button>' +
                '</div>' +
              '</div>' +
              '<div class="vip-tools">' +
                '<div class="vip-form">' +
                  '<label class="field"><span class="lbl">number</span><input id="vip-num" class="input" placeholder="628123456789"></label>' +
                  '<label class="field"><span class="lbl">name</span><input id="vip-name" class="input" placeholder="Name"></label>' +
                  '<label class="field"><span class="lbl">relation</span><input id="vip-rel" class="input" placeholder="Friend"></label>' +
                  '<button class="btn primary" id="btn-vip-add">add</button>' +
                "</div>" +
                '<div class="bulk-form">' +
                  '<label class="field"><span class="lbl">bulk add &mdash; number,name,relation per line</span>' +
                  '<textarea id="vip-bulk" class="input" placeholder="628123456789,Name,Friend"></textarea></label>' +
                  '<button class="btn" id="btn-vip-bulk">add all</button>' +
                "</div>" +
              "</div>" +
              '<table id="vip-table"><thead>' +
                '<tr><th scope="col">number</th><th scope="col">name</th><th scope="col">relation</th><th scope="col" title="whether clark responds to this person">active</th><th scope="col">access</th><th scope="col"></th></tr>' +
              "</thead><tbody></tbody></table>" +
            "</div>" +
            '<div class="card tile-history"><h2>History</h2><p class="sub">recent turns</p>' +
              '<div class="scope-tabs">' +
                '<button data-scope="global">all</button>' +
                '<button data-scope="vip">per vip</button>' +
                '<button data-scope="web" class="active">web</button>' +
              "</div>" +
              '<div id="hist-meta">' +
                '<select id="hist-vip" class="input hidden"></select>' +
                '<label class="check-label">' +
                '<input type="checkbox" id="hist-all"> show more turns</label>' +
                '<div class="spacer"></div>' +
                '<button class="btn mini" id="hist-refresh">refresh</button>' +
              "</div>" +
              '<div class="hist-list" id="hist-list"></div>' +
            "</div>" +
            '<div class="card tile-todos"><h2>Todos</h2><p class="sub">your list — precise, calm, authoritative</p>' +
              '<div class="todo-head">' +
                '<div class="todo-count" id="todo-count">0 open</div>' +
                '<div class="spacer"></div>' +
                '<button class="btn mini" id="todo-refresh">refresh</button>' +
              "</div>" +
              '<form id="todo-form" class="todo-form">' +
                '<input id="todo-input" class="input" placeholder="Add a todo — e.g. Review Tiara’s deck by Friday" aria-label="Add todo">' +
                '<textarea id="todo-desc" class="input" placeholder="Optional description" rows="2" aria-label="Description"></textarea>' +
                '<button class="btn primary" type="submit">add</button>' +
              "</form>" +
              '<div class="todo-list" id="todo-list"></div>' +
              '<div id="todo-pager"></div>' +
            "</div>" +
            '<div class="card tile-calendar"><h2>Calendar</h2><p class="sub">upcoming — precise, calm, authoritative</p>' +
              '<form id="calendar-form" class="todo-form">' +
                '<input id="calendar-title" class="input" placeholder="Event title" aria-label="Event title">' +
                '<input id="calendar-start" class="input" type="datetime-local" aria-label="Start">' +
                '<input id="calendar-end" class="input" type="datetime-local" aria-label="End">' +
                '<button class="btn primary" type="submit">add</button>' +
              "</form>" +
              '<div class="calendar-list" id="calendar-list"><div class="todo-empty">No upcoming events</div></div>' +
              '<button class="btn mini" id="calendar-refresh">refresh</button>' +
            "</div>" +
          "</section>" +
          '<section id="chat" class="hidden">' +
            '<div id="chat-wrap">' +
              '<aside id="chat-sidebar" aria-label="chat sessions">' +
                '<button id="session-new" class="btn primary">+ new chat</button>' +
                '<div id="session-list" role="listbox" aria-label="chat sessions"></div>' +
              "</aside>" +
              '<div id="chat-main">' +
                '<div id="chat-scroll"><div id="chat-list" role="log" aria-live="polite"></div></div>' +
                '<div id="quick-msgs">' +
              '<button class="chip" data-msg="What is your status?">status</button>' +
              '<button class="chip" data-msg="Turn on thinking mode">thinking</button>' +
              '<button class="chip" data-msg="Set my context: Available">context</button>' +
              '<button class="chip" data-msg="Google the latest news today">search</button>' +
              '<button class="chip" data-msg="Show me all the VIPs">vip list</button>' +
              '<button class="chip" data-msg="Send a WhatsApp message to myself saying testing">send msg</button>' +
            '</div>' +
            '<div id="chat-input-bar">' +
              '<textarea id="chat-input" rows="1" placeholder="message clark…" aria-label="message clark"></textarea>' +
              '<button id="chat-send" class="btn primary">send</button>' +
            "</div>" +
              "</div>" +
            "</div>" +
          "</section>" +
          '<section id="kanban" class="hidden">' +
            '<div class="kanban-board">' +
              '<div class="kanban-col" data-status="open"><h3>Open</h3><div class="kanban-list" id="kanban-open"></div></div>' +
              '<div class="kanban-col" data-status="in_progress"><h3>In Progress</h3><div class="kanban-list" id="kanban-doing"></div></div>' +
              '<div class="kanban-col" data-status="closed"><h3>Closed</h3><div class="kanban-list" id="kanban-closed"></div></div>' +
            '</div>' +
            '<div class="kanban-add">' +
              '<input id="kanban-input" class="input" placeholder="Add a todo to kanban…">' +
              '<button class="btn primary" id="kanban-add">add</button>' +
            '</div>' +
          "</section>" +
          '<section id="protocols" class="hidden">' +
            '<div class="section-head"><h2 class="section-title">Protocols <span class="count-chip" id="protocol-count">0</span></h2>' +
            '<p class="sub">Step-by-step procedures clark saves and follows. He reports every protocol he creates himself.</p></div>' +
            '<div id="protocol-list" class="protocol-list"></div>' +
            '<div class="proto-form card">' +
              '<h3>new protocol</h3>' +
              '<label class="field"><span class="lbl">title</span>' +
              '<input id="proto-title" class="input" placeholder="Morning News Digest"></label>' +
              '<label class="field"><span class="lbl">steps</span>' +
              '<textarea id="proto-body" class="input" rows="5" placeholder="1. gather sources&#10;2. summarise&#10;3. report"></textarea></label>' +
              '<button class="btn primary" id="proto-add">save protocol</button>' +
            '</div>' +
            '<div class="section-head"><h2 class="section-title">Schedules <span class="count-chip" id="schedule-count">0</span></h2>' +
            '<p class="sub">Recurring tasks clark runs as you, on cron. He confirms the next run when he creates one.</p></div>' +
            '<div id="schedule-list" class="schedule-list"></div>' +
            '<div class="proto-form card">' +
              '<h3>new schedule</h3>' +
              '<label class="field"><span class="lbl">name</span>' +
              '<input id="sched-name" class="input" placeholder="morning-news"></label>' +
              '<label class="field"><span class="lbl">timing</span></label>' +
              '<div class="sched-kind" role="group" aria-label="schedule kind">' +
                '<button class="btn pager-tab active" data-kind="recurring" aria-pressed="true">repeat</button>' +
                '<button class="btn pager-tab" data-kind="once" aria-pressed="false">once</button>' +
              "</div>" +
              '<div id="sched-repeat" data-pane="repeat">' +
                '<div class="day-pills" id="sched-days" role="group" aria-label="days of week"></div>' +
                '<label class="field"><span class="lbl">time</span>' +
                '<input id="sched-time" class="input" type="time" value="06:00" data-repeat-time></label>' +
              "</div>" +
              '<div id="sched-once" class="hidden" data-pane="once">' +
                '<label class="field"><span class="lbl">date</span>' +
                '<input id="sched-date" class="input" type="date" data-once-date></label>' +
                '<label class="field"><span class="lbl">time</span>' +
                '<input id="sched-time-once" class="input" type="time" value="06:00" data-once-time></label>' +
              "</div>" +
              '<div class="sched-preview" id="sched-preview" data-preview aria-live="polite"></div>' +
              '<details class="sched-adv"><summary>advanced: raw cron</summary>' +
              '<input id="sched-spec" class="input mono" placeholder="0 6 * * *" data-raw-spec">' +
              '<span class="field-hint">Leave empty to use the picker above. Raw spec overrides it.</span></details>' +
              '<label class="field"><span class="lbl">task</span>' +
              '<textarea id="sched-task" class="input" rows="3" placeholder="Run the morning-news protocol: gather current news and report a digest."></textarea></label>' +
              '<button class="btn primary" id="sched-add">save schedule</button>' +
            '</div>' +
          "</section>" +
        "</main>" +
        '<section id="logs">' +
          '<div id="logs-head">' +
            '<span class="title">live log</span>' +
            '<span class="hint" id="logs-hint">streaming…</span>' +
            '<div class="spacer"></div>' +
            '<span class="hint" id="logs-pin">pin</span>' +
          "</div>" +
          '<div id="logs-body" class="hidden"></div>' +
        "</section>" +
      "</div>"
    );
    $("#app").appendChild(shell);
    $("#shell").classList.remove("hidden");

    bindHeader();
    bindBento();
    bindChat();
    bindSessions();
    bindLogs();
    bindProtocols();

    try {
      await api("/web/api/state");
      renderState();
      connectChat();
      connectLogs();
      initChatSessions();
      refreshHistory();
      refreshTodos();
      refreshKanban();
      refreshCalendar();
    } catch (e) {
      toastErr(e.message);
    }
  }

  /* ---------------- header ---------------- */

  function bindHeader() {
    $("#btn-logout").addEventListener("click", logout);
    $("#mode-bento").addEventListener("click", function () { setMode("bento"); });
    $("#mode-chat").addEventListener("click", function () { setMode("chat"); });
    $("#mode-kanban").addEventListener("click", function () { setMode("kanban"); });
    $("#mode-protocols").addEventListener("click", function () { setMode("protocols"); });
  }

  function setMode(m) {
    mode = m;
    $("#mode-bento").classList.toggle("active", m === "bento");
    $("#mode-chat").classList.toggle("active", m === "chat");
    $("#mode-kanban").classList.toggle("active", m === "kanban");
    $("#mode-protocols").classList.toggle("active", m === "protocols");
    $("#mode-bento").setAttribute("aria-pressed", String(m === "bento"));
    $("#mode-chat").setAttribute("aria-pressed", String(m === "chat"));
    $("#mode-kanban").setAttribute("aria-pressed", String(m === "kanban"));
    $("#mode-protocols").setAttribute("aria-pressed", String(m === "protocols"));
    $("#bento").classList.toggle("hidden", m !== "bento");
    $("#chat").classList.toggle("hidden", m !== "chat");
    $("#kanban").classList.toggle("hidden", m !== "kanban");
    $("#protocols").classList.toggle("hidden", m !== "protocols");
    if (m === "chat") $("#chat-input").focus();
    if (m === "kanban") refreshKanban();
    if (m === "protocols") refreshProtocols();
  }

  function markLive(live) {
    const l = $("#live");
    l.classList.toggle("live", live);
    l.innerHTML = '<span class="dot"></span>' + (live ? "live" : "offline");
  }

  /* ---------------- bento handlers ---------------- */

  function bindBento() {
    const cfgStatus = $("#cfg-status");
    const cfgThinking = $("#cfg-thinking");
    const cfgLimit = $("#cfg-limit");
    const cfgForm = $("#config-form");

    cfgStatus.addEventListener("change", function () {
      mutate("/web/api/status", { enabled: cfgStatus.checked }, cfgStatus.checked ? "status on" : "status off");
    });
    cfgThinking.addEventListener("change", function () {
      mutate("/web/api/thinking", { enabled: cfgThinking.checked }, cfgThinking.checked ? "thinking on" : "thinking off");
    });
    cfgLimit.addEventListener("change", function () {
      const n = parseInt(cfgLimit.value, 10);
      if (!n || n < 1 || n > 30) { toastErr("limit must be 1–30"); captureState(); return; }
      mutate("/web/api/history-limit", { limit: n }, "history limit set");
    });
    cfgForm.addEventListener("submit", function (e) {
      e.preventDefault();
      mutate("/web/api/context", { context: $("#cfg-ctx").value }, "context saved");
    });

    $("#btn-vip-add").addEventListener("click", addVIP);
    $("#btn-vip-bulk").addEventListener("click", addVIPBulk);
    $("#vip-picker").addEventListener("change", function () { accessPage = 0; renderAccess(); });
    $("#btn-testtts").addEventListener("click", testTTS);
    $("#voice-toggle").addEventListener("change", onVoiceToggle);
    $("#alert-mode-toggle").addEventListener("change", onAlertModeToggle);

    const tabs = document.querySelectorAll(".scope-tabs button");
    tabs.forEach(function (b) {
      b.addEventListener("click", function () {
        tabs.forEach(function (t) { t.classList.toggle("active", t === b); });
        historyScope = b.dataset.scope;
        historyVip = "";
        historyAll = false;
        $("#hist-all").checked = false;
        $("#hist-vip").classList.toggle("hidden", historyScope !== "vip");
        $("#hist-vip").value = "";
        refreshHistory();
      });
    });
    $("#hist-vip").addEventListener("change", function () {
      historyVip = $("#hist-vip").value;
      historyAll = false;
      $("#hist-all").checked = false;
      refreshHistory();
    });
    $("#hist-all").addEventListener("change", function () {
      historyAll = this.checked;
      refreshHistory();
    });
    $("#hist-refresh").addEventListener("click", refreshHistory);
    const todoRefresh = $("#todo-refresh");
    if (todoRefresh) todoRefresh.addEventListener("click", refreshTodos);
    const todoForm = $("#todo-form");
    if (todoForm) todoForm.addEventListener("submit", addTodo);
    const kanbanAdd = $("#kanban-add");
    if (kanbanAdd) kanbanAdd.addEventListener("click", function () {
      const input = $("#kanban-input");
      const text = input.value.trim();
      if (!text) return;
      api("/web/api/todos", { method: "POST", body: JSON.stringify({ text: text }) }).then(function () { input.value = ""; toastOk("todo added"); refreshTodos(); refreshKanban(); }).catch(function (e) { toastErr(e.message); });
    });
    const calRefresh = $("#calendar-refresh");
    if (calRefresh) calRefresh.addEventListener("click", refreshCalendar);
    const calForm = $("#calendar-form");
    if (calForm) calForm.addEventListener("submit", addCalendarEvent);

    document.querySelectorAll("#vip-sort button").forEach(function (btn) {
      btn.addEventListener("click", function () {
        document.querySelectorAll("#vip-sort button").forEach(function (b) { b.classList.remove("active"); });
        btn.classList.add("active");
        vipSort = btn.dataset.sort;
        renderVips();
      });
    });

    $("#vip-table tbody").addEventListener("click", function (e) {
      const btn = e.target.closest("button[data-vip-action]");
      if (!btn) return;
      const jid = btn.dataset.vip;
      const act = btn.dataset.vipAction;
      if (act === "del") {
        // Two-step guard (shared armDelete): re-renders disarm — fail-safe.
        if (!armDelete(btn)) return;
        mutate("/web/api/vip/delete", { jid: jid }, "deleted " + jid);
      } else if (act === "on") {
        mutate("/web/api/vip/status", { jid: jid, enabled: true }, "vip enabled");
      } else if (act === "off") {
        mutate("/web/api/vip/status", { jid: jid, enabled: false }, "vip disabled");
      }
    });
  }

  async function mutate(path, body, note) {
    try {
      const d = await api(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      if (d && d.error) { toastErr(d.error); return; }
    if (d && d.state) {
      state = d.state;
      captureState();
      renderVips();
      renderAccess();
      renderVoiceMeta();
    }
      if (note) toastOk(note);
    } catch (e) {
      toastErr(e.message);
      captureState();
    }
  }

  async function addVIP() {
    const num = $("#vip-num").value.trim();
    const name = $("#vip-name").value.trim();
    const rel = $("#vip-rel").value.trim();
    if (!num) { toastErr("number required"); return; }
    await mutate("/web/api/vip/add", { input: [num, name, rel].filter(Boolean).join(",") }, "vip added");
    $("#vip-num").value = $("#vip-name").value = $("#vip-rel").value = "";
  }

  async function addVIPBulk() {
    const entries = $("#vip-bulk").value.split("\n").map(function (s) { return s.trim(); }).filter(Boolean);
    if (!entries.length) { toastErr("paste lines first"); return; }
    await mutate("/web/api/vip/add-bulk", { entries: entries }, entries.length + " vips added");
    $("#vip-bulk").value = "";
  }

  /* ---------------- render: voice ---------------- */

  function renderVoiceMeta() {
    const v = state || {};
    $("#voice-stt").textContent = v.sttModel || "\u2014";
    const avail = !!v.ttsEngine;
    // When neither kokoro nor piper is detected, show not detected (instead of kokoro/piper or dash)
    $("#voice-tts").textContent = v.ttsEngine ? v.ttsEngine : "not detected";
    $("#voice-voice").textContent = v.ttsVoice ? v.ttsVoice : (avail ? "\u2014" : "not detected");
    const st = $("#voice-status");
    const toggle = $("#voice-toggle");
    if (!avail) {
      if (st) st.textContent = "voice not detected";
      if (toggle) { toggle.disabled = true; toggle.checked = false; }
    } else if (!voiceOn) {
      if (st) st.textContent = "voice off \u2014 flip the toggle";
      if (toggle) { toggle.disabled = false; toggle.checked = false; }
    } else {
      // voiceOn — keep status meaningful instead of leaving the stale "voice off" text
      if (st && !recording && !clarkSpeaking) st.textContent = "say \u201cclark\u201d";
      if (toggle) { toggle.disabled = false; toggle.checked = true; }
    }
    const amt = $("#alert-mode-toggle");
    if (amt) amt.checked = (v.alertMode !== "silent"); // ON = voice alerts
    $("#btn-testtts").style.visibility = avail ? "" : "hidden";
  }

  function renderVips() {
    const tbody = $("#vip-table tbody");
    let vips = (state && state.vips) || [];
    if (vipSort === "az") vips = vips.slice().sort(function (a, b) { return (a.name || "").localeCompare(b.name || ""); });
    if (!vips.length) {
      tbody.innerHTML = '<tr><td colspan="6" class="empty">no vips yet \u2014 add one above</td></tr>';
    } else {
      tbody.innerHTML = vips.map(function (v) {
        const chips = (v.access || []).map(function (t) {
          return '<span class="chip on">' + esc(t) + "</span>";
        }).join("");
        const toggle = v.enabled
          ? '<button class="btn mini state-on" data-vip="' + esc(v.jid) + '" data-vip-action="off">on</button>'
          : '<button class="btn mini" data-vip="' + esc(v.jid) + '" data-vip-action="on">off</button>';
        return "<tr>" +
          '<td class="jid">' + esc(v.jid) + "</td>" +
          '<td class="name">' + esc(v.name || "\u2014") + "</td>" +
          '<td class="relation">' + esc(v.relation || "\u2014") + "</td>" +
          '<td>' + toggle + "</td>" +
          '<td>' + chips + "</td>" +
          '<td><div class="row-actions"><button class="btn mini" data-vip="' + esc(v.jid) + '" data-vip-action="del">delete</button></div></td>' +
          "</tr>";
      }).join("");
    }

    const picker = $("#vip-picker");
    const cur = picker.value;
    picker.innerHTML = vips.map(function (v) {
      return '<option value="' + esc(v.jid) + '">' + esc(v.name || v.jid) + "</option>";
    }).join("") || '<option value="">no vips</option>';
    if (vips.some(function (v) { return v.jid === cur; })) picker.value = cur;
    refreshVipPicker();
    renderAccess();
  }

  function renderAccess() {
    const picker = $("#vip-picker");
    const jid = picker.value;
    const vips = (state && state.vips) || [];
    const vip = vips.find(function (v) { return v.jid === jid; });
    const tools = (state && state.tools) || [];
    const list = $("#access-list");

    if (!vip) {
      list.innerHTML = '<div class="a-row a-row-empty">pick a vip</div>';
      renderPager($("#access-pager"), [], 0, function () {});
      return;
    }
    const grants = vip.access || [];
    const pagerEl = $("#access-pager");
    const pages = Math.ceil(tools.length / PAGE_SIZE);
    if (accessPage > pages - 1) accessPage = 0;
    const visible = pages > 1 ? tools.slice(accessPage * PAGE_SIZE, (accessPage + 1) * PAGE_SIZE) : tools;
    const labels = [];
    for (let i = 0; i < pages; i++) labels.push(String(i + 1));
    renderPager(pagerEl, labels, accessPage, function (i) { accessPage = i; renderAccess(); });
    list.innerHTML = visible.map(function (t) {
      const name = t && t.name ? t.name : String(t);
      const on = grants.indexOf(name) !== -1;
      return '<div class="a-row"><span class="a-name">' + esc(name) + "</span>" +
        '<label class="switch"><input type="checkbox" data-tool="' + esc(name) + '" data-jid="' + esc(jid) + '"' + (on ? " checked" : "") + ">" +
        '<span class="track"></span><span class="knob"></span></label></div>';
    }).join("") || '<div class="a-row a-row-empty">no tools</div>';

    list.querySelectorAll("input[data-tool]").forEach(function (cb) {
      cb.addEventListener("change", function () {
        mutate("/web/api/access", { jid: cb.dataset.jid, tool: cb.dataset.tool, enabled: cb.checked }, "access updated");
      });
    });
  }

  function renderChatMeta() {
    const v = state || {};
    $("#chat-list").innerHTML = "";
    const who = v.name || "clark";
    const seed = el(
      '<div class="msg clark"><div class="bubble">' +
        "Hi, I\u2019m " + esc(who) + ". Ask me anything \u2014 I have " + esc((v.tools || []).length) + " tools and a long memory." +
        '<span class="meta">' + esc(v.model || "") + "</span>" +
        "</div></div>"
    );
    $("#chat-list").appendChild(seed);
  }

  /* ---------------- chat sessions ---------------- */

  function persistChatSession() {
    try {
      if (chatSessionId) localStorage.setItem("clark.chatSession", String(chatSessionId));
      else localStorage.removeItem("clark.chatSession");
    } catch (e) { /* private mode */ }
  }

  async function refreshSessions() {
    // Never wipe a rename draft: live updates wait until it saves/cancels.
    if (document.querySelector(".session-row.renaming")) return;
    try {
      const d = await api("/web/api/chat/sessions");
      chatSessions = (d && d.sessions) || [];
      if (!chatSessions.some(function (s) { return s.id === chatSessionId; })) {
        chatSessionId = chatSessions.length ? chatSessions[0].id : 0;
        persistChatSession();
      }
      renderSessions();
    } catch (e) {
      if (e.message !== "session expired") toastErr(e.message);
    }
  }

  function renderSessions() {
    const list = $("#session-list");
    if (!list) return;
    if (!chatSessions.length) {
      list.innerHTML = '<div class="todo-empty">no chats</div>';
      return;
    }
    list.innerHTML = chatSessions.map(function (s) {
      const active = s.id === chatSessionId;
      return '<div class="session-row' + (active ? " active" : "") + '" role="option" aria-selected="' + String(active) + '" data-id="' + s.id + '" tabindex="0" title="' + esc(s.preview || s.title) + '">' +
        '<div class="session-text"><span class="session-title">' + esc(s.title) + "</span>" +
        (s.preview ? '<span class="session-preview">' + esc(s.preview) + "</span>" : "") +
        "</div>" +
        '<button class="session-del" data-id="' + s.id + '" aria-label="delete ' + esc(s.title) + '">×</button>' +
        "</div>";
    }).join("");
    list.querySelectorAll(".session-row").forEach(function (row) {
      row.addEventListener("click", function (e) {
        if (e.target.closest(".session-del")) return;
        if (e.target.closest(".session-rename")) return;
        switchSession(+row.dataset.id);
      });
      row.addEventListener("keydown", function (e) {
        if (e.key === "Enter" && !e.target.closest(".session-rename")) switchSession(+row.dataset.id);
      });
      row.addEventListener("dblclick", function (e) {
        if (e.target.closest(".session-del")) return;
        startSessionRename(row);
      });
    });
    list.querySelectorAll(".session-del").forEach(function (btn) {
      btn.addEventListener("click", function () {
        if (!armDelete(btn)) return;
        const id = +btn.dataset.id;
        api("/web/api/chat/sessions/" + id, { method: "DELETE" })
          .then(function (d) {
            toastOk("chat deleted");
            if (id === chatSessionId) {
              chatSessionId = (d && d.next && d.next.id) || 0;
              persistChatSession();
              loadSessionTranscript();
            }
            refreshSessions();
          })
          .catch(function (err) { toastErr(err.message); });
      });
    });
  }

  function startSessionRename(row) {
    const id = +row.dataset.id;
    const cur = chatSessions.find(function (s) { return s.id === id; });
    if (!cur) return;
    row.classList.add("renaming");
    const text = row.querySelector(".session-text");
    text.innerHTML = '<input class="input session-rename" value="' + esc(cur.title) + '" aria-label="rename chat">';
    const inp = text.querySelector("input");
    inp.focus();
    inp.select();
    function done(save) {
      row.classList.remove("renaming");
      if (!save) { renderSessions(); return; }
      const title = inp.value.trim();
      if (!title || title === cur.title) { renderSessions(); return; }
      api("/web/api/chat/sessions/" + id, { method: "PUT", body: JSON.stringify({ title: title }) })
        .then(function () { toastOk("chat renamed"); refreshSessions(); })
        .catch(function (err) { toastErr(err.message); renderSessions(); });
    }
    inp.addEventListener("keydown", function (e) {
      if (e.key === "Enter") done(true);
      else if (e.key === "Escape") done(false);
    });
    inp.addEventListener("blur", function () { done(true); });
  }

  async function switchSession(id) {
    if (id === chatSessionId && $("#chat-list").children.length) {
      renderSessions();
      return;
    }
    chatSessionId = id;
    persistChatSession();
    renderSessions();
    await loadSessionTranscript();
  }

  async function loadSessionTranscript() {
    const list = $("#chat-list");
    list.innerHTML = "";
    if (!chatSessionId) {
      renderChatMeta();
      return;
    }
    try {
      const d = await api("/web/api/chat/sessions/" + chatSessionId + "/messages");
      const msgs = (d && d.messages) || [];
      if (!msgs.length) {
        renderChatMeta();
        return;
      }
      msgs.forEach(function (m) {
        appendChat(m.role === "user" ? "user" : "clark", m.content || "");
      });
      scrollChat();
    } catch (e) {
      renderChatMeta();
      if (e.message !== "session expired") toastErr(e.message);
    }
  }

  async function initChatSessions() {
    await refreshSessions();
    await loadSessionTranscript();
  }

  function bindSessions() {
    const btn = $("#session-new");
    if (btn) btn.addEventListener("click", async function () {
      try {
        const d = await api("/web/api/chat/sessions", { method: "POST", body: JSON.stringify({}) });
        if (d && d.session) {
          chatSessionId = d.session.id;
          persistChatSession();
          toastOk("new chat");
          await refreshSessions();
          await loadSessionTranscript();
          $("#chat-input").focus();
        }
      } catch (e) {
        toastErr(e.message);
      }
    });
  }

  /* ---------------- history ---------------- */

  async function refreshHistory() {
    if (historyLoading) return;
    historyLoading = true;
    try {
      const params = "?scope=" + encodeURIComponent(historyScope);
      let q = params;
      if (historyScope === "vip") {
        if (historyVip) q += "&jid=" + encodeURIComponent(historyVip);
        else { $("#hist-list").innerHTML = '<div id="hist-empty">pick a vip above</div>'; return; }
      }
      if (historyAll) q += "&limit=200";
      const d = await api("/web/api/history" + q);
      const entries = (d && d.entries) || [];
      const list = $("#hist-list");
      if (!entries.length) {
        list.innerHTML = '<div id="hist-empty">no history yet</div>';
        return;
      }
      // The 15s auto-refresh swaps innerHTML; keep the reader's scroll
      // position so the list doesn't jump back to the top mid-read.
      const keepScroll = list.scrollTop;
      list.innerHTML = entries.map(function (e) {
        const who = e.role === "user" ? "you" : "clark";
        const t = e.time || "";
        return '<div class="hist-row">' +
          '<span class="hist-time">' + esc(t) + "</span>" +
          '<span class="hist-who ' + (e.role === "user" ? "you" : "ck") + '">' + who + "</span>" +
          '<span class="hist-text">' + esc(e.content) + "</span>" +
          "</div>";
      }).join("");
      list.scrollTop = keepScroll;
    } catch (e) {
      if (e.message !== "session expired") toastErr(e.message);
    } finally {
      historyLoading = false;
    }
  }

  function refreshVipPicker() {
    const vips = (state && state.vips) || [];
    const sel = $("#hist-vip");
    const cur = sel.value;
    sel.innerHTML = vips.map(function (v) {
      return '<option value="' + esc(v.jid) + '">' + esc(v.name || v.jid) + "</option>";
    }).join("");
    if (vips.some(function (v) { return v.jid === cur; })) sel.value = cur;
  }

  /* ---------------- todos ---------------- */

  async function refreshTodos() {
    try {
      const d = await api("/web/api/todos");
      const todos = (d && d.todos) || [];
      renderTodos(todos);
    } catch (e) {
      if (e.message !== "session expired") toastErr(e.message);
    }
  }

  function renderTodos(todos) {
    const list = $("#todo-list");
    const count = $("#todo-count");
    const open = todos.filter(function (t) { return t.status === "open"; }).length;
    count.textContent = open + " open";
    const pagerEl = $("#todo-pager");
    if (!todos.length) {
      list.innerHTML = '<div class="todo-empty">No todos yet — add one above</div>';
      renderPager(pagerEl, [], 0, function () {});
      return;
    }
    const pages = Math.ceil(todos.length / PAGE_SIZE);
    if (todoPage > pages - 1) todoPage = 0;
    const visible = pages > 1 ? todos.slice(todoPage * PAGE_SIZE, (todoPage + 1) * PAGE_SIZE) : todos;
    const labels = [];
    for (let i = 0; i < pages; i++) labels.push(String(i + 1));
    renderPager(pagerEl, labels, todoPage, function (i) { todoPage = i; refreshTodos(); });
    list.innerHTML = visible.map(function (t) {
      const closed = t.status === "closed" || t.status === "done";
      const prio = t.priority || 0;
      const due = t.due_at ? new Date(t.due_at).toLocaleDateString() : "";
      const desc = t.description ? '<div class="todo-desc">' + esc(t.description) + '</div>' : "";
      return '<div class="todo-row' + (closed ? " done" : "") + '">' +
        '<button class="todo-check' + (closed ? " done" : "") + '" data-id="' + t.id + '" data-done="' + closed + '" aria-label="toggle done"></button>' +
        '<div style="flex:1"><span class="todo-text' + (closed ? " done" : "") + '">' + esc(t.text) + "</span>" + desc + "</div>" +
        '<span class="todo-meta">' +
          '<span class="todo-prio p' + Math.min(prio, 3) + '"></span>' +
          (due ? "<span>" + esc(due) + "</span>" : "") +
        "</span>" +
        '<button class="todo-del" data-id="' + t.id + '" aria-label="delete">×</button>' +
        "</div>";
    }).join("");
    list.querySelectorAll(".todo-check").forEach(function (btn) {
      btn.addEventListener("click", function () {
        const id = btn.dataset.id;
        const done = btn.dataset.done === "true";
        if (done) return;
        api("/web/api/todos/" + id + "/complete", { method: "POST" }).then(function () { toastOk("todo completed"); refreshTodos(); refreshKanban(); }).catch(function (e) { toastErr(e.message); });
      });
    });
    list.querySelectorAll(".todo-del").forEach(function (btn) {
      btn.addEventListener("click", function () {
        if (!armDelete(btn)) return;
        const id = btn.dataset.id;
        api("/web/api/todos/" + id, { method: "DELETE" }).then(function () { toastOk("todo deleted"); refreshTodos(); refreshKanban(); }).catch(function (e) { toastErr(e.message); });
      });
    });
  }

  async function addTodo(e) {
    e.preventDefault();
    const input = $("#todo-input");
    const descInput = $("#todo-desc");
    const text = input.value.trim();
    const desc = descInput ? descInput.value.trim() : "";
    if (!text) return;
    try {
      await api("/web/api/todos", { method: "POST", body: JSON.stringify({ text: text, description: desc }) });
      input.value = "";
      if (descInput) descInput.value = "";
      toastOk("todo added");
      refreshTodos();
      refreshKanban();
    } catch (err) {
      toastErr(err.message);
    }
  }

  /* ---------------- kanban ---------------- */

  async function refreshKanban() {
    try {
      const d = await api("/web/api/todos");
      const todos = (d && d.todos) || [];
      renderKanban(todos);
    } catch (e) {
      if (e.message !== "session expired") toastErr(e.message);
    }
  }

  function renderKanban(todos) {
    const openList = $("#kanban-open");
    const doingList = $("#kanban-doing");
    const closedList = $("#kanban-closed");
    if (!openList || !closedList || !doingList) return;
    const open = todos.filter(function (t) { return t.status === "open"; });
    const doing = todos.filter(function (t) { return t.status === "in_progress"; });
    const closed = todos.filter(function (t) { return t.status === "closed"; });
    openList.innerHTML = open.map(function (t) { return kanbanCard(t); }).join("") || '<div class="todo-empty">No open todos</div>';
    doingList.innerHTML = doing.map(function (t) { return kanbanCard(t); }).join("") || '<div class="todo-empty">No tasks in progress</div>';
    closedList.innerHTML = closed.map(function (t) { return kanbanCard(t); }).join("") || '<div class="todo-empty">No closed todos</div>';
    attachKanbanHandlers();
  }

  function kanbanCard(t) {
    const prio = t.priority || 0;
    const due = t.due_at ? new Date(t.due_at).toLocaleDateString() : "";
    const closed = t.status === "closed";
    const doing = t.status === "in_progress";
    const desc = t.description ? '<div class="todo-desc kanban-desc">' + esc(t.description) + '</div>' : "";
    const statusLabel = closed ? "closed" : doing ? "in progress" : "open";
    return '<div class="kanban-card' + (closed ? " closed" : doing ? " doing" : "") + '" draggable="true" tabindex="0" data-id="' + t.id + '" aria-label="' + esc(t.text) + ", status " + statusLabel + ". Press left or right arrow to move." + '">' +
      '<div class="todo-text' + (closed ? " done" : "") + '">' + esc(t.text) + "</div>" + desc +
      '<div class="todo-meta">' +
        '<span class="todo-prio p' + Math.min(prio, 3) + '"></span>' +
        (due ? "<span>" + esc(due) + "</span>" : "") +
        '<span class="spacer"></span>' +
        '<button class="todo-del" data-id="' + t.id + '">×</button>' +
      "</div>" +
      "</div>";
  }

  function attachKanbanHandlers() {
    const statusOrder = ["open", "in_progress", "closed"];
    document.querySelectorAll(".kanban-card").forEach(function (card) {
      card.addEventListener("dragstart", function (e) {
        e.dataTransfer.setData("text/plain", card.dataset.id);
        card.classList.add("dragging");
      });
      card.addEventListener("dragend", function () { card.classList.remove("dragging"); });
      // Keyboard path: left/right arrows walk the card through the columns.
      card.addEventListener("keydown", function (e) {
        if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
        const col = card.closest(".kanban-col");
        if (!col) return;
        const idx = statusOrder.indexOf(col.dataset.status);
        const next = statusOrder[idx + (e.key === "ArrowRight" ? 1 : -1)];
        if (!next || next === col.dataset.status) return;
        e.preventDefault();
        api("/web/api/todos/" + card.dataset.id + "/status", { method: "POST", body: JSON.stringify({ status: next }) })
          .then(function () { toastOk("todo moved"); refreshTodos(); refreshKanban(); })
          .catch(function (err) { toastErr(err.message); });
      });
    });
    document.querySelectorAll(".kanban-col").forEach(function (col) {
      col.addEventListener("dragover", function (e) { e.preventDefault(); col.classList.add("drag-over"); });
      col.addEventListener("dragleave", function () { col.classList.remove("drag-over"); });
      col.addEventListener("drop", function (e) {
        e.preventDefault();
        col.classList.remove("drag-over");
        const id = e.dataTransfer.getData("text/plain");
        const status = col.dataset.status;
        if (!id || !status) return;
        api("/web/api/todos/" + id + "/status", { method: "POST", body: JSON.stringify({ status: status }) }).then(function () { toastOk("todo moved"); refreshTodos(); refreshKanban(); }).catch(function (err) { toastErr(err.message); });
      });
    });
    document.querySelectorAll(".kanban-card .todo-del").forEach(function (btn) {
      btn.addEventListener("click", function (e) {
        e.stopPropagation();
        if (!armDelete(btn)) return;
        const id = btn.dataset.id || btn.closest(".kanban-card").dataset.id;
        api("/web/api/todos/" + id, { method: "DELETE" }).then(function () { toastOk("todo deleted"); refreshTodos(); refreshKanban(); }).catch(function (err) { toastErr(err.message); });
      });
    });
  }

  /* ---------------- protocols + schedules ---------------- */

  // armDelete: first tap arms the button ("confirm?") for 3s; the second tap
  // actually fires. Returns true only when the tap should proceed.
  function armDelete(btn) {
    if (btn.dataset.armed === "1") return true;
    btn.dataset.armed = "1";
    if (!btn.dataset.orig) btn.dataset.orig = btn.textContent;
    btn.textContent = "confirm?";
    btn.classList.add("armed");
    setTimeout(function () {
      if (!btn.isConnected || btn.dataset.armed !== "1") return;
      btn.dataset.armed = "";
      btn.textContent = btn.dataset.orig;
      btn.classList.remove("armed");
    }, 3000);
    return false;
  }

  // humanizeCron renders common 5-field specs in plain words; unknown
  // patterns return "" and the raw spec stands alone.
  function humanizeCron(spec) {
    const p = (spec || "").trim().split(/\s+/);
    if (p.length !== 5) return "";
    const min = p[0], hr = p[1], dom = p[2], mon = p[3], dow = p[4];
    const days = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
    const hhmm = function (h, m) { return String(h).padStart(2, "0") + ":" + String(m).padStart(2, "0"); };
    if (/^\d+$/.test(min) && /^\d+$/.test(hr) && dom === "*" && mon === "*") {
      if (dow === "*") return "daily at " + hhmm(hr, min);
      var parts = dow.split(",");
      var allNum = parts.length > 0;
      for (var i = 0; i < parts.length; i++) {
        if (!/^\d+$/.test(parts[i])) { allNum = false; break; }
      }
      if (allNum) {
        var names = parts.map(function (p) { return days[+p % 7]; });
        if (names.length === 7) return "daily at " + hhmm(hr, min);
        return names.join(", ") + " at " + hhmm(hr, min);
      }
    }
    if (min.startsWith("*/") && hr === "*" && dom === "*" && mon === "*" && dow === "*") {
      return "every " + min.slice(2) + " min";
    }
    if (min === "0" && hr.startsWith("*/") && dom === "*" && mon === "*" && dow === "*") {
      return "every " + hr.slice(2) + " h";
    }
    return "";
  }

  async function refreshProtocols() {
    // Never wipe an in-progress edit: the websocket fires *_changed on any
    // save anywhere, and a blind re-render would destroy the draft.
    if (document.querySelector(".proto-card.proto-editing")) return;
    try {
      const data = await api("/web/api/protocols");
      renderProtocols(data.protocols || []);
    } catch (err) {
      toastErr(err.message);
    }
    refreshSchedules();
  }

  async function refreshSchedules() {
    // Own guard so a protocol draft never blocks schedule updates and
    // vice versa.
    if (document.querySelector(".sched-row.sched-editing")) return;
    try {
      const sdata = await api("/web/api/schedules");
      renderSchedules(sdata.schedules || []);
    } catch (err) {
      toastErr(err.message);
    }
  }

  function originChip(origin) {
    return origin === "clark"
      ? '<span class="origin-chip clark">clark</span>'
      : '<span class="origin-chip">master</span>';
  }

  function renderProtocols(protocols) {
    const list = $("#protocol-list");
    const count = $("#protocol-count");
    if (count) count.textContent = String(protocols.length);
    if (!protocols.length) {
      list.innerHTML = '<div class="empty-state"><strong>No protocols yet.</strong><br>' +
        "After Clark solves something reusable, tell him <em>save that as a protocol</em> — " +
        "it lands here, and he follows it next time.</div>";
      return;
    }
    list.innerHTML = protocols.map(function (p) {
      return (
        '<div class="proto-card card" data-id="' + p.id + '">' +
          '<div class="proto-head">' +
            '<div class="proto-heading">' +
              '<span class="proto-title">' + esc(p.title) + "</span>" +
              originChip(p.origin) +
              '<span class="proto-meta">v' + p.version + " · used " + p.use_count + "×</span>" +
            "</div>" +
            '<div class="proto-actions">' +
              '<button class="btn proto-edit" data-id="' + p.id + '">edit</button>' +
              '<button class="btn proto-del" data-id="' + p.id + '">delete</button>' +
            "</div>" +
          "</div>" +
          '<div class="proto-slug">' + esc(p.slug) + "</div>" +
          '<pre class="proto-view">' + esc(p.body) + "</pre>" +
          '<input class="input proto-title-input hidden" data-id="' + p.id + '" value="' + esc(p.title) + '" aria-label="Protocol title">' +
          '<textarea class="input proto-body-input hidden" data-id="' + p.id + '" rows="8" aria-label="Protocol body for ' + esc(p.title) + '">' + esc(p.body) + "</textarea>" +
          '<button class="btn primary proto-save hidden" data-id="' + p.id + '">save changes</button>' +
        "</div>"
      );
    }).join("");
    list.querySelectorAll(".proto-edit").forEach(function (btn) {
      btn.addEventListener("click", function () {
        const card = btn.closest(".proto-card");
        const editing = card.classList.toggle("proto-editing");
        btn.textContent = editing ? "cancel" : "edit";
        const titleInput = card.querySelector(".proto-title-input");
        const ta = card.querySelector(".proto-body-input");
        if (editing) {
          titleInput.value = card.querySelector(".proto-title").textContent;
          ta.value = card.querySelector(".proto-view").textContent;
          titleInput.classList.remove("hidden");
          ta.classList.remove("hidden");
          card.querySelector(".proto-save").classList.remove("hidden");
          titleInput.focus();
        } else {
          titleInput.classList.add("hidden");
          ta.classList.add("hidden");
          card.querySelector(".proto-save").classList.add("hidden");
        }
      });
    });
    list.querySelectorAll(".proto-save").forEach(function (btn) {
      btn.addEventListener("click", function () {
        const id = btn.dataset.id;
        const card = btn.closest(".proto-card");
        const body = card.querySelector(".proto-body-input").value;
        const title = card.querySelector(".proto-title-input").value.trim();
        if (!title || !body.trim()) { toastErr("title and body are required"); return; }
        btn.disabled = true;
        api("/web/api/protocols/" + id, { method: "PUT", body: JSON.stringify({ title: title, body: body }) })
          .then(function () {
            // Clear the draft guard BEFORE re-rendering or the fresh list is skipped.
            card.classList.remove("proto-editing");
            toastOk("protocol saved");
            refreshProtocols();
          })
          .catch(function (err) { toastErr(err.message); btn.disabled = false; });
      });
    });
    list.querySelectorAll(".proto-del").forEach(function (btn) {
      btn.addEventListener("click", function () {
        if (!armDelete(btn)) return;
        api("/web/api/protocols/" + btn.dataset.id, { method: "DELETE" })
          .then(function () { toastOk("protocol deleted"); refreshProtocols(); })
          .catch(function (err) { toastErr(err.message); });
      });
    });
  }

  var SCHED_DAYS = [["Mon", 1], ["Tue", 2], ["Wed", 3], ["Thu", 4], ["Fri", 5], ["Sat", 6], ["Sun", 0]];

  function fmtDateInput(dt) {
    var p = function (n) { return String(n).padStart(2, "0"); };
    return dt.getFullYear() + "-" + p(dt.getMonth() + 1) + "-" + p(dt.getDate());
  }
  function fmtTimeInput(dt) {
    var p = function (n) { return String(n).padStart(2, "0"); };
    return p(dt.getHours()) + ":" + p(dt.getMinutes());
  }

  function schedDayPillsHTML(selected) {
    return SCHED_DAYS.map(function (d) {
      var on = selected.indexOf(d[1]) !== -1;
      return '<button type="button" class="btn pager-tab day-pill' + (on ? " active" : "") + '" data-day="' + d[1] + '"' +
        ' aria-pressed="' + String(on) + '">' + d[0] + "</button>";
    }).join("");
  }

  function bindDayPills(root) {
    root.querySelectorAll(".day-pill").forEach(function (b) {
      b.addEventListener("click", function () {
        b.classList.toggle("active");
        b.setAttribute("aria-pressed", String(b.classList.contains("active")));
        var form = b.closest("[data-sched-form]");
        if (form) updateSchedPreview(form);
      });
    });
  }

  function schedFormDays(form) {
    var days = [];
    form.querySelectorAll(".day-pill.active").forEach(function (b) { days.push(+b.dataset.day); });
    return days;
  }

  // parseSpecTiming extracts picker values from a plain "M H * * DOW" spec.
  // Returns {days, time} or null when the spec is too complex for the picker
  // (those keep the raw-spec field instead).
  function parseSpecTiming(spec) {
    var p = (spec || "").trim().split(/\s+/);
    if (p.length !== 5) return null;
    if (!/^\d+$/.test(p[0]) || !/^\d+$/.test(p[1])) return null;
    if (p[2] !== "*" || p[3] !== "*") return null;
    var time = String(p[1]).padStart(2, "0") + ":" + String(p[0]).padStart(2, "0");
    var days;
    if (p[4] === "*") {
      days = [0, 1, 2, 3, 4, 5, 6];
    } else {
      days = [];
      var parts = p[4].split(",");
      for (var i = 0; i < parts.length; i++) {
        if (!/^\d+$/.test(parts[i])) return null;
        days.push(+parts[i] % 7);
      }
    }
    return { days: days, time: time };
  }

  function schedKindOf(form) {
    var active = form.querySelector('.sched-kind [data-kind].active');
    return active ? active.dataset.kind : "recurring";
  }

  function setSchedKind(form, kind) {
    form.querySelectorAll(".sched-kind [data-kind]").forEach(function (b) {
      var on = b.dataset.kind === kind;
      b.classList.toggle("active", on);
      b.setAttribute("aria-pressed", String(on));
    });
    var rep = form.querySelector("[data-pane=repeat]");
    var once = form.querySelector("[data-pane=once]");
    if (rep) rep.classList.toggle("hidden", kind !== "recurring");
    if (once) once.classList.toggle("hidden", kind !== "once");
    updateSchedPreview(form);
  }

  function updateSchedPreview(form) {
    var prev = form.querySelector("[data-preview]");
    if (!prev) return;
    var kind = schedKindOf(form);
    if (kind === "once") {
      var d = form.querySelector("[data-once-date]");
      var t = form.querySelector("[data-once-time]");
      var dv = d && d.value ? d.value : "";
      var tv = t && t.value ? t.value : "";
      if (!dv || !tv) { prev.textContent = "pick a date and time"; return; }
      var dt = new Date(dv + "T" + tv);
      if (isNaN(dt.getTime())) { prev.textContent = "invalid date"; return; }
      prev.textContent = "runs once on " + dt.toLocaleString([], { weekday: "short", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
      return;
    }
    var raw = form.querySelector("[data-raw-spec]");
    if (raw && raw.value.trim()) {
      var human = humanizeCron(raw.value.trim());
      prev.textContent = human ? "runs " + human : "runs on " + raw.value.trim();
      return;
    }
    var days = schedFormDays(form);
    var timeEl = form.querySelector("[data-repeat-time]");
    var time = timeEl && timeEl.value ? timeEl.value : "";
    if (!days.length) { prev.textContent = "pick at least one day"; return; }
    if (!time) { prev.textContent = "pick a time"; return; }
    var hhmm = time.split(":");
    var spec = parseInt(hhmm[1], 10) + " " + parseInt(hhmm[0], 10) + " * * " +
      (days.length === 7 ? "*" : days.slice().sort(function (a, b) { return a - b; }).join(","));
    var h = humanizeCron(spec);
    prev.textContent = h ? "runs " + h : "runs on " + spec;
  }

  function schedPayload(form, name, task) {
    var kind = schedKindOf(form);
    var body = { name: name, task: task };
    if (kind === "once") {
      var d = form.querySelector("[data-once-date]");
      var t = form.querySelector("[data-once-time]");
      if (!d || !d.value || !t || !t.value) return { error: "date and time are required for a one-time schedule" };
      body.kind = "once";
      body.run_at = d.value + "T" + t.value;
      return { body: body };
    }
    var raw = form.querySelector("[data-raw-spec]");
    if (raw && raw.value.trim()) {
      body.spec = raw.value.trim();
      return { body: body };
    }
    var days = schedFormDays(form);
    var timeEl = form.querySelector("[data-repeat-time]");
    if (!days.length) return { error: "pick at least one day" };
    if (!timeEl || !timeEl.value) return { error: "pick a time" };
    body.kind = "recurring";
    body.days = days;
    body.time = timeEl.value;
    return { body: body };
  }

  function renderSchedules(schedules) {
    const list = $("#schedule-list");
    const count = $("#schedule-count");
    if (count) count.textContent = String(schedules.length);
    if (!schedules.length) {
      list.innerHTML = '<div class="empty-state"><strong>No schedules.</strong><br>' +
        "Ask Clark: <em>gather the news and report to me every day at 6 AM</em> — " +
        "he creates the cron schedule himself.</div>";
      return;
    }
    list.innerHTML = schedules.map(function (sc) {
      const once = sc.kind === "once";
      const human = once ? "" : humanizeCron(sc.spec);
      const next = sc.next_run ? new Date(sc.next_run).toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" }) : "—";
      const last = sc.last_run_at ? new Date(sc.last_run_at).toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" }) : "never";
      const state = sc.enabled ? '<span class="origin-chip on">running</span>' : '<span class="origin-chip">paused</span>';
      const kindChip = '<span class="kind-chip">' + (once ? "once" : "repeat") + "</span>";
      var when;
      if (once && sc.run_at) {
        when = "once · " + esc(new Date(sc.run_at).toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }));
      } else {
        when = esc(sc.spec) + (human ? " · " + esc(human) : "");
      }
      // Edit-form prefill: picker values for plain specs, raw field for
      // complex ones, date/time for one-time jobs.
      var timing = parseSpecTiming(sc.spec || "");
      var editDays = timing ? timing.days : [0, 1, 2, 3, 4, 5, 6];
      var editTime = timing ? timing.time : "06:00";
      var editRaw = timing ? "" : (sc.spec || "");
      var editDate = "";
      var editOnceTime = "06:00";
      if (once && sc.run_at) {
        var rdt = new Date(sc.run_at);
        if (!isNaN(rdt.getTime())) {
          editDate = fmtDateInput(rdt);
          editOnceTime = fmtTimeInput(rdt);
        }
      }
      return (
        '<div class="sched-row card" data-name="' + esc(sc.name) + '">' +
          '<div class="proto-head">' +
            '<div class="proto-heading">' +
              '<span class="proto-title">' + esc(sc.name) + "</span>" +
              state +
              kindChip +
              '<span class="proto-meta mono">' + when + "</span>" +
            "</div>" +
            '<div class="proto-actions">' +
              '<button class="btn sched-edit" data-name="' + esc(sc.name) + '">edit</button>' +
              '<button class="btn sched-toggle" data-name="' + esc(sc.name) + '" data-enabled="' + (sc.enabled ? "1" : "0") + '">' + (sc.enabled ? "pause" : "resume") + "</button>" +
              '<button class="btn sched-del" data-name="' + esc(sc.name) + '">delete</button>' +
            "</div>" +
          "</div>" +
          '<div class="sched-when">next <span>' + esc(next) + "</span><span class='dot-sep'>·</span>last <span>" + esc(last) + "</span></div>" +
          '<div class="sched-task">' + esc(sc.task || "") + "</div>" +
          '<div class="sched-edit-form hidden" data-sched-form>' +
            '<label class="field"><span class="lbl">name</span>' +
            '<input class="input" data-edit-name value="' + esc(sc.name) + '"></label>' +
            '<div class="sched-kind" role="group" aria-label="schedule kind">' +
              '<button type="button" class="btn pager-tab' + (once ? "" : " active") + '" data-kind="recurring" aria-pressed="' + String(!once) + '">repeat</button>' +
              '<button type="button" class="btn pager-tab' + (once ? " active" : "") + '" data-kind="once" aria-pressed="' + String(once) + '">once</button>' +
            "</div>" +
            '<div data-pane="repeat"' + (once ? ' class="hidden"' : "") + ">" +
              '<div class="day-pills">' + schedDayPillsHTML(editDays) + "</div>" +
              '<label class="field"><span class="lbl">time</span>' +
              '<input class="input" type="time" data-repeat-time value="' + esc(editTime) + '"></label>' +
              '<details class="sched-adv"><summary>advanced: raw cron</summary>' +
              '<input class="input mono" data-raw-spec value="' + esc(editRaw) + '" placeholder="0 6 * * *"></details>' +
            "</div>" +
            '<div data-pane="once"' + (once ? "" : ' class="hidden"') + ">" +
              '<label class="field"><span class="lbl">date</span>' +
              '<input class="input" type="date" data-once-date value="' + esc(editDate) + '"></label>' +
              '<label class="field"><span class="lbl">time</span>' +
              '<input class="input" type="time" data-once-time value="' + esc(editOnceTime) + '"></label>' +
            "</div>" +
            '<div class="sched-preview" data-preview aria-live="polite"></div>' +
            '<label class="field"><span class="lbl">task</span>' +
            '<textarea class="input" data-edit-task rows="3">' + esc(sc.task || "") + "</textarea></label>" +
            '<div class="proto-actions"><button class="btn primary sched-save" data-name="' + esc(sc.name) + '">save changes</button>' +
            '<button class="btn sched-cancel">cancel</button></div>' +
          "</div>" +
        "</div>"
      );
    }).join("");
    bindDayPills(list);
    list.querySelectorAll(".sched-kind [data-kind]").forEach(function (b) {
      b.addEventListener("click", function () {
        var form = b.closest("[data-sched-form]");
        if (form) setSchedKind(form, b.dataset.kind);
      });
    });
    list.querySelectorAll("[data-sched-form] input, [data-sched-form] textarea").forEach(function (inp) {
      inp.addEventListener("input", function () {
        var form = inp.closest("[data-sched-form]");
        if (form) updateSchedPreview(form);
      });
    });
    list.querySelectorAll(".sched-edit").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var card = btn.closest(".sched-row");
        var editing = card.classList.toggle("sched-editing");
        btn.textContent = editing ? "cancel" : "edit";
        var form = card.querySelector("[data-sched-form]");
        form.classList.toggle("hidden", !editing);
        if (editing) updateSchedPreview(form);
      });
    });
    list.querySelectorAll(".sched-cancel").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var card = btn.closest(".sched-row");
        card.classList.remove("sched-editing");
        card.querySelector("[data-sched-form]").classList.add("hidden");
        card.querySelector(".sched-edit").textContent = "edit";
      });
    });
    list.querySelectorAll(".sched-save").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var card = btn.closest(".sched-row");
        var form = card.querySelector("[data-sched-form]");
        var newName = form.querySelector("[data-edit-name]").value.trim();
        var task = form.querySelector("[data-edit-task]").value;
        if (!newName) { toastErr("name is required"); return; }
        var res = schedPayload(form, newName, task);
        if (res.error) { toastErr(res.error); return; }
        if (newName !== btn.dataset.name) res.body.new_name = newName;
        btn.disabled = true;
        api("/web/api/schedules/" + encodeURIComponent(btn.dataset.name), { method: "PUT", body: JSON.stringify(res.body) })
          .then(function () {
            card.classList.remove("sched-editing");
            toastOk("schedule updated");
            refreshSchedules();
          })
          .catch(function (err) { toastErr(err.message); btn.disabled = false; });
      });
    });
    list.querySelectorAll(".sched-toggle").forEach(function (btn) {
      btn.addEventListener("click", function () {
        const enable = btn.dataset.enabled !== "1";
        api("/web/api/schedules/" + encodeURIComponent(btn.dataset.name), { method: "PUT", body: JSON.stringify({ enabled: enable }) })
          .then(function () { toastOk(enable ? "schedule resumed" : "schedule paused"); refreshSchedules(); })
          .catch(function (err) { toastErr(err.message); });
      });
    });
    list.querySelectorAll(".sched-del").forEach(function (btn) {
      btn.addEventListener("click", function () {
        if (!armDelete(btn)) return;
        api("/web/api/schedules/" + encodeURIComponent(btn.dataset.name), { method: "DELETE" })
          .then(function () { toastOk("schedule deleted"); refreshSchedules(); })
          .catch(function (err) { toastErr(err.message); });
      });
    });
  }

  // initSchedCreate wires the builder controls of the new-schedule card:
  // kind pills, day pills, and the live preview.
  function initSchedCreate() {
    var card = $("#sched-add");
    if (!card) return;
    var form = card.closest(".proto-form");
    form.setAttribute("data-sched-form", "");
    var daysEl = $("#sched-days");
    if (daysEl && !daysEl.children.length) {
      daysEl.innerHTML = schedDayPillsHTML([0, 1, 2, 3, 4, 5, 6]);
      // Owned by the static create form, not a re-rendered list.
      daysEl.querySelectorAll(".day-pill").forEach(function (b) {
        b.addEventListener("click", function () {
          b.classList.toggle("active");
          b.setAttribute("aria-pressed", String(b.classList.contains("active")));
          updateSchedPreview(form);
        });
      });
    }
    form.querySelectorAll(".sched-kind [data-kind]").forEach(function (b) {
      b.addEventListener("click", function () { setSchedKind(form, b.dataset.kind); });
    });
    ["#sched-time", "#sched-date", "#sched-time-once", "#sched-spec", "#sched-task"].forEach(function (sel) {
      var inp = $(sel);
      if (inp) inp.addEventListener("input", function () { updateSchedPreview(form); });
    });
    var dateEl = $("#sched-date");
    if (dateEl && !dateEl.value) {
      var now = new Date();
      dateEl.value = fmtDateInput(new Date(now.getTime() + 86400000));
    }
    updateSchedPreview(form);
  }

  function resetSchedCreate() {
    $("#sched-name").value = "";
    $("#sched-spec").value = "";
    $("#sched-task").value = "";
    var form = $("#sched-add").closest(".proto-form");
    setSchedKind(form, "recurring");
    form.querySelectorAll("#sched-days .day-pill").forEach(function (b) {
      b.classList.add("active");
      b.setAttribute("aria-pressed", "true");
    });
    updateSchedPreview(form);
  }

  function bindProtocols() {
    initSchedCreate();
    $("#proto-add").addEventListener("click", function () {
      const title = $("#proto-title").value.trim();
      const body = $("#proto-body").value;
      if (!title || !body) { toastErr("title and body are required"); return; }
      api("/web/api/protocols", { method: "POST", body: JSON.stringify({ title: title, body: body, origin: "master" }) })
        .then(function () {
          $("#proto-title").value = "";
          $("#proto-body").value = "";
          toastOk("protocol saved");
          refreshProtocols();
        })
        .catch(function (err) { toastErr(err.message); });
    });
    $("#sched-add").addEventListener("click", function () {
      const form = $("#sched-add").closest(".proto-form");
      const name = $("#sched-name").value.trim();
      const task = $("#sched-task").value;
      if (!name) { toastErr("name is required"); return; }
      const res = schedPayload(form, name, task);
      if (res.error) { toastErr(res.error); return; }
      if (!task.trim()) { toastErr("task is required for a new schedule"); return; }
      api("/web/api/schedules", { method: "POST", body: JSON.stringify(res.body) })
        .then(function () {
          resetSchedCreate();
          toastOk("schedule saved");
          refreshSchedules();
        })
        .catch(function (err) { toastErr(err.message); });
    });
  }

  /* ---------------- calendar (tool-driven) ---------------- */

  /* ---------------- calendar tile (direct REST) ---------------- */

  async function refreshCalendar() {
    const list = $("#calendar-list");
    try {
      const d = await api("/web/api/calendar");
      renderCalendar(d.events || []);
    } catch (err) {
      list.innerHTML = '<div class="empty-state">' + esc(err.message) + "</div>";
    }
  }

  // renderPager: shared list pager (calendar days, todo pages, tool pages).
  // labels[] is one tab per page; page is the active index. onPage(i)
  // re-renders content only — no refetch. Tabs are native buttons, so the
  // row is keyboard-operable; arrows move pages when focus is inside.
  function renderPager(el, labels, page, onPage) {
    if (!el) return;
    if (labels.length <= 1) { el.innerHTML = ""; return; }
    el.innerHTML = '<button class="btn pager-btn" data-p="prev" aria-label="previous page">‹</button>' +
      labels.map(function (l, i) {
        return '<button class="btn pager-tab' + (i === page ? " active" : "") + '" data-p="' + i + '"' +
          (i === page ? ' aria-current="page"' : "") + ">" + esc(l) + "</button>";
      }).join("") +
      '<button class="btn pager-btn" data-p="next" aria-label="next page">›</button>';
    el.querySelectorAll("button").forEach(function (btn) {
      btn.addEventListener("click", function () {
        const p = btn.dataset.p;
        if (p === "prev") onPage((page + labels.length - 1) % labels.length);
        else if (p === "next") onPage((page + 1) % labels.length);
        else onPage(+p);
      });
    });
    el.onkeydown = function (e) {
      if (e.key === "ArrowLeft") { e.preventDefault(); onPage((page + labels.length - 1) % labels.length); }
      else if (e.key === "ArrowRight") { e.preventDefault(); onPage((page + 1) % labels.length); }
    };
  }

  let calDays = []; // [{key,label,short,items}] — exactly the 7 window days
  let calPage = 0;

  let todoPage = 0;
  let accessPage = 0;
  const PAGE_SIZE = 8;

  function renderCalendar(events) {
    const list = $("#calendar-list");
    if (!events.length) {
      calDays = [];
      list.innerHTML = '<div class="empty-state"><strong>Nothing scheduled this week.</strong><br>' +
        "Add an event above, or ask Clark in chat — <em>what's on my calendar?</em></div>";
      return;
    }
    const now = new Date();
    const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
    const byDay = {};
    events.forEach(function (e) {
      const s = new Date(e.start);
      const key = s.getFullYear() + "-" + String(s.getMonth() + 1).padStart(2, "0") + "-" + String(s.getDate()).padStart(2, "0");
      (byDay[key] = byDay[key] || []).push(e);
    });
    calDays = [];
    for (let i = 0; i < 7; i++) {
      const d = new Date(today.getTime() + i * 86400000);
      const key = d.getFullYear() + "-" + String(d.getMonth() + 1).padStart(2, "0") + "-" + String(d.getDate()).padStart(2, "0");
      calDays.push({
        key: key,
        label: i === 0 ? "Today" : i === 1 ? "Tomorrow" :
          d.toLocaleDateString([], { weekday: "long", month: "short", day: "numeric" }),
        short: i === 0 ? "Today" : i === 1 ? "Tom." :
          d.toLocaleDateString([], { weekday: "short", day: "numeric" }),
        items: byDay[key] || [],
      });
    }
    if (calPage < 0 || calPage > 6) calPage = 0;
    renderCalPage();
  }

  function renderCalPage() {
    const list = $("#calendar-list");
    if (!calDays.length) {
      list.innerHTML = '<div class="empty-state"><strong>Nothing scheduled this week.</strong><br>' +
        "Add an event above, or ask Clark in chat — <em>what's on my calendar?</em></div>";
      return;
    }
    const g = calDays[calPage];
    let html = '<div id="cal-pager" role="navigation" aria-label="calendar days"></div>';
    html += '<div class="calendar-day"><div class="calendar-day-label">' + esc(g.label) + "</div>";
    html += g.items.length ? g.items.map(function (e) {
      const s = new Date(e.start);
      const t = e.allDay ? "all day" :
        s.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) + "–" +
        new Date(e.end).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
      return '<div class="calendar-event">' +
        '<span class="calendar-event-title">' + esc(e.title) + (e.location ? '<span class="calendar-event-loc">@ ' + esc(e.location) + "</span>" : "") + "</span>" +
        '<span class="calendar-event-time">' + esc(t) + "</span>" +
        "</div>";
    }).join("") : '<div class="todo-empty">Nothing scheduled this day.</div>';
    html += "</div>";
    list.innerHTML = html;
    renderPager($("#cal-pager"), calDays.map(function (d) { return d.short; }), calPage, function (i) {
      calPage = i;
      renderCalPage();
    });
  }
  async function addCalendarEvent(e) {
    e.preventDefault();
    const title = $("#calendar-title").value.trim();
    const startV = $("#calendar-start").value;
    const endV = $("#calendar-end").value;
    if (!title || !startV || !endV) { toastErr("title, start, and end are required"); return; }
    const start = new Date(startV);
    const end = new Date(endV);
    if (!(end > start)) { toastErr("end must be after start"); return; }
    try {
      await api("/web/api/calendar/events", {
        method: "POST",
        body: JSON.stringify({ title: title, start: start.toISOString(), end: end.toISOString() }),
      });
      $("#calendar-title").value = "";
      $("#calendar-start").value = "";
      $("#calendar-end").value = "";
      toastOk("event added");
      refreshCalendar();
    } catch (err) {
      toastErr(err.message);
    }
  }

  /* ---------------- idle sound ---------------- */

  let idleSource = null;
  let idleBuffer = null;

  function ensureAudioCtx() {
    if (!audioCtx) {
      audioCtx = new (window.AudioContext || window.webkitAudioContext)();
    }
    if (audioCtx.state === "suspended") audioCtx.resume();
  }

  async function loadIdleBuffer() {
    if (idleBuffer) return idleBuffer;
    try {
      ensureAudioCtx();
      const resp = await fetch("/web/affirmations/idle.wav");
      const buf = await resp.arrayBuffer();
      idleBuffer = await audioCtx.decodeAudioData(buf);
      return idleBuffer;
    } catch (e) { return null; }
  }

  function startIdle() {
    if (!voiceOn || idleSource) return;
    loadIdleBuffer().then(function (decoded) {
      if (!decoded || !voiceOn) return;
      idleSource = audioCtx.createBufferSource();
      idleSource.buffer = decoded;
      idleSource.loop = true;
      idleSource.connect(audioCtx.destination);
      idleSource.start();
    });
  }

  function stopIdle() {
    if (idleSource) {
      try { idleSource.stop(); } catch (e) {}
      idleSource.disconnect();
      idleSource = null;
    }
  }

  /* ---------------- chat (ws) ---------------- */

  function connectChat() {
    if (chatWs && (chatWs.readyState === WebSocket.OPEN || chatWs.readyState === WebSocket.CONNECTING)) {
      try { chatWs.close(); } catch (e) {}
    }
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const ws = new WebSocket(proto + "//" + location.host + "/web/api/chat");
    chatWs = ws;
    ws.onopen = function () {
      chatBackoff = 1000;
      sendFrame("auth", { token: token });
      markLive(true);
    };
    ws.onclose = function () {
      markLive(false);
      // Exponential retry (1s doubling to an 8s cap) so a dead server isn't
      // hammered; resets on the next successful open.
      const delay = chatBackoff;
      chatBackoff = Math.min(chatBackoff * 2, 8000);
      if (ws === chatWs) setTimeout(connectChat, delay);
    };
    ws.onerror = function () { ws.close(); };
    let streamBubble = null;
    let streamText = "";
    let streamDone = false;
    ws.onmessage = function (ev) {
      let f;
      try { f = JSON.parse(ev.data); } catch (e) { return; }
      if (f.type === "ack") {
        setTyping(false);
        // Create the bubble shell with typing dots.
        const who = "clark";
        const meta = who + " \u00b7 " + new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
        const li = el(
          '<div class="msg ' + who + '">' +
            '<div class="bubble">' +
              '<div class="typing-dots"><span></span><span></span><span></span></div>' +
              '<span class="meta">' + meta + "</span>" +
            "</div>" +
          "</div>"
        );
        $("#chat-list").appendChild(li);
        scrollChat();
        streamBubble = li.querySelector(".bubble");
        streamText = "";
        streamDone = false;
        stopSpeech(); // cut any prior-turn audio so its queued sentences drop
        spokenUpTo = 0;
        startIdle();
      } else if (f.type === "thinking") {
        if (streamBubble) {
          const det = el(
            '<details class="thinking-block">' +
              '<summary>reasoning</summary>' +
              '<div class="thinking-text">' + renderMarkup(f.text || "") + "</div>" +
            "</details>"
          );
          const meta = streamBubble.querySelector(".meta");
          if (meta) streamBubble.insertBefore(det, meta);
          else streamBubble.appendChild(det);
          scrollChat();
        }
      } else if (f.type === "token" && !streamDone) {
        if (streamBubble) {
          const dots = streamBubble.querySelector(".typing-dots");
          if (dots) dots.remove();
          let textNode = streamBubble.querySelector(".stream-text");
          if (!textNode) {
            textNode = el("<span class='stream-text'></span>");
            const meta = streamBubble.querySelector(".meta");
            if (meta) streamBubble.insertBefore(textNode, meta);
            else streamBubble.appendChild(textNode);
          }
          textNode.textContent += f.text;
          streamText += f.text;
          scrollChat();
          // Early TTS: speak each newly-completed stable sentence exactly once.
          if (voiceOn && !streamDone) {
            var s;
            while ((s = nextStableSentence(streamText, spokenUpTo)) !== null) {
              enqueueSpeech(s.text, speechGen);
              spokenUpTo = s.end;
            }
          }
        }
      } else if (f.type === "done") {
        streamDone = true;
        chatBusy = false;
        stopIdle();
        if (streamBubble) {
          const dots = streamBubble.querySelector(".typing-dots");
          if (dots) dots.remove();
          const textNode = streamBubble.querySelector(".stream-text");
          if (textNode) textNode.outerHTML = renderMarkup(streamText);
          // Speak any remaining text not yet queued (incomplete final sentence).
          if (voiceOn && spokenUpTo < streamText.length) {
            var tail = streamText.slice(spokenUpTo).trim();
            if (tail) enqueueSpeech(tail, speechGen);
          }
          refreshAfterTurn();
        }
        streamBubble = null;
        streamText = "";
        spokenUpTo = 0;
        spokenCount = 0;
        // Turn landed in this session — sidebar preview/title may have changed.
        refreshSessions();
        // If TTS had nothing to play (empty reply or fetch failure), playBuffer
        // never fired its onended fallback. Ensure wake resumes.
        setTimeout(function () {
          if (voiceOn && !clarkSpeaking && !recording && !wakeHeld && !wakeRecognition) startWake();
        }, 150);
      } else if (f.type === "reply") {
        // Fallback: if streaming already populated the bubble, ignore this.
        // If no bubble exists (streaming skipped/failed), create one.
        if (!streamBubble && !streamDone) {
          chatBusy = false;
          appendChat("clark", f.text || "");
          if (voiceOn && f.text) speakTTS(f.text);
          refreshAfterTurn();
          refreshSessions();
        }
      } else if (f.type === "error") {
        chatBusy = false;
        setTyping(false);
        stopIdle();
        if (streamBubble) {
          const dots = streamBubble.querySelector(".typing-dots");
          if (dots) dots.remove();
          const meta = streamBubble.querySelector(".meta");
          const errEl = el('<div class="err-msg">' + renderMarkup(f.message || "something went wrong") + "</div>");
          if (meta) streamBubble.insertBefore(errEl, meta);
          else streamBubble.appendChild(errEl);
          refreshAfterTurn();
        } else {
          appendChat("clark", f.message || "something went wrong");
        }
        streamBubble = null;
        streamText = "";
        streamDone = false;
        setTimeout(function () {
          if (voiceOn && !clarkSpeaking && !recording && !wakeHeld && !wakeRecognition) startWake();
        }, 150);
      } else if (f.type === "alert") {
        // Server-initiated alert (bypass command, monitoring webhook). Always
        // render it as a clark message and log it to the console. In voice
        // mode (speak=true) also read it aloud (auto-toggle voice on, speak,
        // restore); in silent mode keep the console silent.
        if (f.text) {
          appendChat("clark", f.text);
          if (f.speak !== false) speakAlert(f.text);
          else stopIdle();
          refreshAfterTurn();
        }
      } else if (f.type === "state") {
        // Server pushes a fresh state snapshot whenever any setting changes
        // (status, context, thinking, alert mode, VIPs, access). Reflect it
        // immediately instead of waiting for the slow safety poll.
        if (f.state) {
          state = f.state;
          renderState();
        }
      } else if (f.type === "protocols_changed") {
        // A protocol was saved/edited/deleted (chat tool or another console)
        // — refresh the Protocols page live if it is open.
        if (mode === "protocols") refreshProtocols();
      } else if (f.type === "schedules_changed") {
        if (mode === "protocols") refreshSchedules();
      } else if (f.type === "sessions_changed") {
        if (mode === "chat") refreshSessions();
      } else if (f.type === "pong") {
        /* keepalive ok */
      }
    };
  }

  function sendFrame(type, payload, ws) {
    ws = ws || chatWs;
    if (!ws || ws.readyState !== WebSocket.OPEN) return false;
    const f = Object.assign({ type: type }, payload);
    ws.send(JSON.stringify(f));
    return true;
  }

  function scrollChat() {
    var scroll = $("#chat-scroll");
    if (scroll) scroll.scrollTop = scroll.scrollHeight;
  }

  function appendChat(who, text) {
    const li = el(
      '<div class="msg ' + who + '"><div class="bubble">' + renderMarkup(text) +
        '<span class="meta">' + who + " \u00b7 " + new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) + "</span>" +
        "</div></div>"
    );
    const list = $("#chat-list");
    list.appendChild(li);
    scrollChat();
  }

  function setTyping(on) {
    const existing = $("#chat-list .typing");
    if (on && !existing) {
      const li = el('<div class="msg clark typing"><div class="bubble"><div class="typing-dots"><span></span><span></span><span></span></div></div></div>');
      $("#chat-list").appendChild(li);
      scrollChat();
    } else if (!on && existing) {
      existing.remove();
    }
  }

  function renderMarkup(text) {
    let s = esc(text);

    // Fenced code blocks -> <pre><code>, held aside so their newlines survive.
    const blocks = [];
    s = s.replace(/```[^\n]*\n?([\s\S]*?)```/g, function (_, body) {
      blocks.push("<pre><code>" + body + "</code></pre>");
      return "\u0000B" + (blocks.length - 1) + "\u0000";
    });

    // Inline code -> placeholders (protects * and _ inside code).
    const codes = [];
    s = s.replace(/`([^`\n]+)`/g, function (_, c) {
      codes.push(c);
      return "\u0000C" + (codes.length - 1) + "\u0000";
    });

    s = s.replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>");
    s = s.replace(/\*([^*\n]+)\*/g, "<em>$1</em>");
    s = s.replace(/_([^_\n]+)_/g, "<em>$1</em>");

    // Bullet and numbered lists, line by line. Line breaks are emitted here
    // so list markup and <pre> blocks never get stray <br> injected.
    const lines = s.split("\n");
    const out = [];
    let inList = null;
    for (let i = 0; i < lines.length; i++) {
      const line = lines[i];
      const last = i === lines.length - 1;

      let m = line.match(/^\s*[-*]\s+/);
      if (m) {
        if (inList !== "ul") { if (inList) out.push("</ol>"); out.push("<ul>"); inList = "ul"; }
        out.push("<li>" + line.replace(/^\s*[-*]\s+/, "") + "</li>");
        continue;
      }
      m = line.match(/^\s*\d+\.\s+/);
      if (m) {
        if (inList !== "ol") { if (inList) out.push("</ul>"); out.push("<ol>"); inList = "ol"; }
        out.push("<li>" + line.replace(/^\s*\d+\.\s+/, "") + "</li>");
        continue;
      }
      if (inList) { out.push(inList === "ul" ? "</ul>" : "</ol>"); inList = null; }
      out.push(line + (last ? "" : "<br>"));
    }
    if (inList) out.push(inList === "ul" ? "</ul>" : "</ol>");

    s = out.join("");
    s = s.replace(/\u0000C(\d+)\u0000/g, function (_, i) { return "<code>" + codes[+i] + "</code>"; });
    s = s.replace(/\u0000B(\d+)\u0000/g, function (_, i) { return blocks[+i]; });
    return s;
  }

  function bindChat() {
    const input = $("#chat-input");
    const send = $("#chat-send");

    document.querySelectorAll("#quick-msgs .chip").forEach(function (chip) {
      chip.addEventListener("click", function () {
        input.value = chip.dataset.msg;
        input.style.height = "auto";
        input.style.height = Math.min(input.scrollHeight, 140) + "px";
        input.focus();
      });
    });

    function updateQuickMsgs() {
      var q = $("#quick-msgs");
      if (q) q.style.display = input.value.trim() ? "none" : "";
    }
    input.addEventListener("input", updateQuickMsgs);
    updateQuickMsgs();

    async function submit() {
      const text = input.value.trim();
      if (!text || chatBusy) return;
      if (!chatWs || chatWs.readyState !== WebSocket.OPEN) {
        toastErr("chat link offline");
        return;
      }
      chatBusy = true;
      input.value = "";
      input.style.height = "auto";
      appendChat("user", text);
      setTyping(true);
      if (!sendFrame("chat", { text: text, session_id: chatSessionId })) {
        chatBusy = false;
        setTyping(false);
        toastErr("could not send");
      }
    }
    send.addEventListener("click", submit);
    input.addEventListener("keydown", function (e) {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        submit();
      }
    });
    input.addEventListener("input", function () {
      input.style.height = "auto";
      input.style.height = Math.min(input.scrollHeight, 140) + "px";
    });
  }

  /* ---------------- logs (ws) ---------------- */

  function connectLogs() {
    // Never open a second live socket over an existing one — overlapping
    // connections double every log line in the strip.
    if (logsWs && (logsWs.readyState === WebSocket.OPEN || logsWs.readyState === WebSocket.CONNECTING)) {
      try { logsWs.close(); } catch (e) {}
    }
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const ws = new WebSocket(proto + "//" + location.host + "/web/api/logs");
    logsWs = ws;
    const body = $("#logs-body");
    ws.onopen = function () {
      logsBackoff = 1000;
      sendFrame("auth", { token: token }, ws);
      const h = $("#logs-hint");
      if (h) h.textContent = "streaming\u2026";
    };
    ws.onclose = function () {
      const h = $("#logs-hint");
      if (h) h.textContent = "offline \u2014 retrying";
      const delay = logsBackoff;
      logsBackoff = Math.min(logsBackoff * 2, 8000);
      if (ws === logsWs) setTimeout(connectLogs, delay);
    };
    ws.onmessage = function (ev) {
      let f;
      try { f = JSON.parse(ev.data); } catch (e) { return; }
      if (f.type === "replay") {
        body.innerHTML = "";
        (f.lines || []).forEach(function (l) { appendLog(l); });
        if (!logsOpen) body.classList.add("hidden");
      } else if (f.type === "log") {
        appendLog(f.line);
      }
    };
  }

  function appendLog(raw) {
    const line = esc(raw || "");
    const cls = logClass(raw);
    const body = $("#logs-body");
    const div = el('<div class="line ' + cls + '">' + line + "</div>");
    body.appendChild(div);
    while (body.childNodes.length > 400) body.removeChild(body.firstChild);
    if (!logsPaused) body.scrollTop = body.scrollHeight;
  }

  function logClass(raw) {
    const s = String(raw || "");
    // Plain line format: "TIMESTAMP LEVEL COMPONENT EVENT: message" — colour by
    // the severity word so streams are readable instead of uniformly blue.
    const m = s.match(/^\s*\S+\s+(\S+)\s+/);
    const level = (m && m[1]) || "";
    if (/^(ERROR|FATAL|PANIC)/i.test(level)) return "err";
    if (/^WARN/i.test(level)) return "warn";
    if (/^DEBUG/i.test(level)) return "faint";
    if (/^NOTICE/i.test(level)) return "info";
    if (/^INFO/i.test(level)) return "ok";
    // Fallback for lines without the standard shape.
    const low = s.toLowerCase();
    if (/\b(error|fail|panic|fatal)\b/.test(low)) return "err";
    if (/\b(warn|slow|retry)\b/.test(low)) return "warn";
    if (/\b(connected|ready|success)\b/.test(low)) return "ok";
    return "";
  }

  function bindLogs() {
    const head = $("#logs-head");
    const body = $("#logs-body");
    const pin = $("#logs-pin");

    function toggleOpen() {
      logsOpen = !logsOpen;
      body.classList.toggle("hidden", !logsOpen);
      head.setAttribute("aria-expanded", String(logsOpen));
      if (logsOpen) body.scrollTop = body.scrollHeight;
    }

    // The strip header and pin are div/span for layout freedom; give them
    // button semantics so keyboard users can operate the log console.
    head.setAttribute("role", "button");
    head.tabIndex = 0;
    head.setAttribute("aria-expanded", "false");
    head.addEventListener("click", function (e) {
      if (e.target.closest("#logs-pin")) return;
      toggleOpen();
    });
    head.addEventListener("keydown", function (e) {
      if (e.key !== "Enter" && e.key !== " ") return;
      e.preventDefault();
      toggleOpen();
    });

    function togglePin() {
      logsPinned = !logsPinned;
      pin.style.textDecoration = logsPinned ? "underline" : "";
      pin.setAttribute("aria-pressed", String(logsPinned));
      body.classList.toggle("paused", logsPinned);
    }
    pin.setAttribute("role", "switch");
    pin.tabIndex = 0;
    pin.setAttribute("aria-pressed", "false");
    pin.addEventListener("click", togglePin);
    pin.addEventListener("keydown", function (e) {
      if (e.key !== "Enter" && e.key !== " ") return;
      e.preventDefault();
      e.stopPropagation(); // keep the head's keydown from also toggling open
      togglePin();
    });
    body.addEventListener("mouseenter", function () {
      if (!logsPinned) { logsPaused = true; body.classList.add("paused"); }
    });
    body.addEventListener("mouseleave", function () {
      if (!logsPinned) { logsPaused = false; body.classList.remove("paused"); }
    });
  }

  /* ---------------- voice ---------------- */

  const AFFIRMATIONS = [
    "Sir.",
    "Listening, Sir.",
    "Right here, Sir.",
    "Yes, Sir?",
    "At your service, Sir.",
    "I\u2019m here, Sir.",
    "How can I help, Sir?",
    "Ready when you are, Sir.",
    "Standing by, Sir.",
    "Go ahead, Sir."
  ];

  async function testTTS() {
    const status = $("#voice-status");
    if (status) status.textContent = "speaking\u2026";
    try {
      ensureAudioCtx();
      if (audioCtx.state === "suspended") await audioCtx.resume();
      const d = await api("/web/api/tts", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text: "Hello, I\u2019m Clark. Voice is working." }),
      });
      if (!d || !d.audio) throw new Error("no audio");
      var binary = atob(d.audio);
      var bytes = new Uint8Array(binary.length);
      for (var i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
      var buf = await audioCtx.decodeAudioData(bytes.buffer);
      clarkSpeaking = true;
      try { stopWake(); } catch (e) {}
      var src = audioCtx.createBufferSource();
      src.buffer = buf;
      src.connect(audioCtx.destination);
      speechSource = src;
      src.onended = function () {
        speechSource = null;
        clarkSpeaking = false;
        if (status) status.textContent = voiceOn ? "say \u201cclark\u201d" : "voice off \u2014 flip the toggle";
        if (voiceOn && !recording && !wakeHeld) startWake();
      };
      src.start();
    } catch (e) {
      if (status) status.textContent = e.message;
    }
  }

  async function onVoiceToggle() {
    if ($("#voice-toggle").checked) await armVoice();
    else disarmVoice();
  }

  // onAlertModeToggle persists the alert delivery mode ("voice" = speak alerts
  // aloud; "silent" = show on WhatsApp/iMessage/web, buzz via FaceTime+banner).
  // The switch reads as: ON = voice alerts, OFF = silent alerts.
  async function onAlertModeToggle() {
    const voice = $("#alert-mode-toggle").checked;
    try {
      await api("/web/api/alert-mode", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ mode: voice ? "voice" : "silent" }),
      });
      renderVoiceMeta();
      toastOk(voice ? "alerts will speak" : "alerts silent");
    } catch (e) {
      toastErr(e.message);
      $("#alert-mode-toggle").checked = !voice;
    }
  }

  async function armVoice() {
    const status = $("#voice-status");
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
      toastErr("mic unavailable in this browser");
      $("#voice-toggle").checked = false;
      return;
    }
    try {
      micStream = await navigator.mediaDevices.getUserMedia({ audio: true });
    } catch (e) {
      toastErr("mic permission denied: " + e.message);
      $("#voice-toggle").checked = false;
      if (status) status.textContent = "mic unavailable";
      return;
    }
    voiceOn = true;
    localStorage.setItem("clark-voiceOn", "true");
    ensureAudioCtx();
    setupAnalyser();
    loadIdleBuffer(); // preload so idle is instant on ack
    if (status) status.textContent = "say \u201cclark\u201d";
    startWake();
    // startWake may bail on clarkSpeaking/wakeRecognition; ensure the pill
    // reflects the real toggle even when the mic hasn't re-armed yet. The
    // playBuffer/playClip onended fallback will re-arm once the clip clears.
    if (voiceOn && status && status.textContent.indexOf("voice off") !== -1) {
      status.textContent = "say \u201cclark\u201d";
    }
  }

  function disarmVoice() {
    disarming = true;
    voiceOn = false;
    localStorage.setItem("clark-voiceOn", "false");
    wakeHeld = false;
    stopSpeech();
    stopWake();
    stopRecording();
    if (micStream) {
      micStream.getTracks().forEach(function (t) { t.stop(); });
      micStream = null;
    }
    const status = $("#voice-status");
    if (status) status.textContent = "voice off \u2014 flip the toggle";
  }

  function setupAnalyser() {
    if (!micStream) return;
    audioCtx = audioCtx || new (window.AudioContext || window.webkitAudioContext)();
    const src = audioCtx.createMediaStreamSource(micStream);
    analyser = audioCtx.createAnalyser();
    analyser.fftSize = 2048;
    src.connect(analyser);
    if (audioCtx.state === "suspended") audioCtx.resume();
  }

  function startWake() {
    if (!voiceOn || wakeRecognition || wakeHeld || clarkSpeaking) return;
    const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
    const status = $("#voice-status");
    if (!SR) {
      if (status) status.textContent = "wake word unsupported \u2014 use a Chromium browser";
      return;
    }
    const r = new SR();
    r.continuous = true;
    r.interimResults = true;
    r.lang = "en-US";
    r.onresult = function (ev) {
      for (let i = ev.resultIndex; i < ev.results.length; i++) {
        const t = (ev.results[i][0] || {}).transcript || "";
        if (t.toLowerCase().indexOf("clark") !== -1) { handleWake(); return; }
      }
    };
    r.onerror = function () {};
    r.onend = function () {
      wakeRecognition = null;
      if (!clarkSpeaking && voiceOn && !recording && !wakeHeld) startWake();
    };
    r.start();
    wakeRecognition = r;
    if (status) status.textContent = "say \u201cclark\u201d";
  }

  function stopWake() {
    if (wakeRecognition) {
      const r = wakeRecognition;
      wakeRecognition = null;
      try { r.stop(); } catch (e) {}
    }
  }

  function handleWake() {
    if (recording || wakeHeld || clarkSpeaking) return;
    stopWake();
    wakeHeld = true;
    const idx = Math.floor(Math.random() * AFFIRMATIONS.length);
    const status = $("#voice-status");
    if (status) status.textContent = "\u201c" + AFFIRMATIONS[idx] + "\u201d";
    // Pre-rendered clip — no server round-trip, plays instantly.
    playClip((idx < 10 ? "0" + idx : idx) + ".wav").then(function () {
      startRecording();
      wakeHeld = false;
    });
  }

  function playClip(file) {
    return new Promise(function (resolve) {
      clarkSpeaking = true;
      try { stopWake(); } catch (e) {}
      const a = new Audio("/web/affirmations/" + file);
      activeClips.add(a);
      a.onended = function () { activeClips.delete(a); clarkSpeaking = false; resolve(); };
      a.onerror = function () { activeClips.delete(a); clarkSpeaking = false; resolve(); };
      a.play();
    });
  }

  function fetchTTSBuffer(text) {
    ensureAudioCtx();
    return api("/web/api/tts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text: text }),
    }).then(function (d) {
      if (!d || !d.audio) return null;
      var resume = audioCtx.state === "suspended" ? audioCtx.resume() : Promise.resolve();
      return resume.then(function () {
        var binary = atob(d.audio);
        var bytes = new Uint8Array(binary.length);
        for (var i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
        return audioCtx.decodeAudioData(bytes.buffer);
      });
    }).catch(function () { return null; });
  }

  function playBuffer(buffer) {
    if (!buffer) return Promise.resolve();
    return new Promise(function (resolve) {
      clarkSpeaking = true;
      try { stopWake(); } catch (e) {}
      var src = audioCtx.createBufferSource();
      src.buffer = buffer;
      src.connect(audioCtx.destination);
      speechSource = src;
      src.onended = function () {
        speechSource = null;
        clarkSpeaking = false;
        if (voiceOn && !recording && !wakeHeld) startWake();
        resolve();
      };
      src.start();
    });
  }

  // stopSpeech cuts off any in-flight reply immediately: bumps the generation
  // (so queued, not-yet-played chunks are dropped) and stops the active source
  // mid-word (barge-in / voice toggle off).
  function stopSpeech() {
    speechGen++;
    clarkSpeaking = false;
    activeClips.forEach(function (a) { try { a.pause(); a.src = ""; } catch (e) {} });
    activeClips.clear();
    playChain = Promise.resolve();
    if (speechSource) {
      try { speechSource.stop(); } catch (e) {}
      speechSource = null;
    }
  }

  // splitSentences splits on sentence-ending punctuation while ignoring
  // punctuation inside backticked code, [links](urls), and decimals.
  function splitSentences(text) {
    if (!text) return [];
    var out = [];
    var buf = "";
    var inBack = false;
    var bracketDepth = 0;
    var inUrl = false;
    var urlTail = "";
    for (var i = 0; i < text.length; i++) {
      var c = text[i];
      buf += c;
      if (c === "`") inBack = !inBack;
      else if (c === "[") bracketDepth++;
      else if (c === "]") bracketDepth = Math.max(0, bracketDepth - 1);
      urlTail = (urlTail + c).slice(-4);
      if (urlTail === "://") inUrl = true;
      if (inUrl && /\s/.test(c)) inUrl = false;
      var isEnd = c === "." || c === "!" || c === "?";
      if (isEnd && !inBack && bracketDepth === 0 && !inUrl) {
        var prev = i > 0 ? text[i - 1] : "";
        var next = i < text.length - 1 ? text[i + 1] : "";
        var decimal = c === "." && /[0-9]/.test(prev) && /[0-9]/.test(next);
        if (!decimal && (i === text.length - 1 || /\s/.test(next))) {
          var t = buf.trim();
          if (t) out.push(t);
          buf = "";
        }
      }
    }
    var tail = buf.trim();
    if (tail) out.push(tail);
    return out;
  }

  // ---- Serial speech playback ---------------------------------------------
  // Sentences start their TTS fetch IMMEDIATELY and concurrently (the synthesis
  // PROCESS runs in parallel with playback of earlier sentences), but playback
  // is appended to playChain so each sentence only PLAYS after the previous one
  // finishes. This keeps latency low (Clark talks on the first sentence) while
  // preventing overlap/garble. stopSpeech() bumps speechGen so stale chain links
  // are skipped. playChain is declared with the other voice globals above.
  function enqueueSpeech(text, gen) {
    if (!text || !voiceOn) return;
    if (gen !== speechGen) return; // stale (voice off / superseded by newer turn)
    // Kick off synthesis now, in parallel with everything else.
    const fetchPromise = fetchTTSBuffer(text);
    // Sequence only the PLAYBACK.
    playChain = playChain.then(async function () {
      if (gen !== speechGen) return; // dropped by stopSpeech()
      const buf = await fetchPromise;
      if (gen !== speechGen) return;
      if (buf) {
        await playBuffer(buf);
      } else {
        // No audio (fetch failed or empty) — ensure wake can resume
        clarkSpeaking = false;
        if (voiceOn && !recording && !wakeHeld && !wakeRecognition) startWake();
      }
    });
  }

  // nextStableSentence returns the next complete, stable sentence starting at
  // `from`, or null. "Stable" means the terminal punctuation is followed by
  // whitespace AND the next word does NOT start with a capital letter — so
  // "Dr. Smith", "Mr. Jones", "U.S." are not chopped mid-abbreviation. Decimals
  // (3.14) are not split either.
  function nextStableSentence(text, from) {
    for (let i = from; i < text.length; i++) {
      const c = text[i];
      if (c !== "." && c !== "!" && c !== "?") continue;
      const prev = i > 0 ? text[i - 1] : "";
      const after = text.slice(i + 1);
      let j = 0;
      while (j < after.length && /\s/.test(after[j])) j++;
      const nextChar = j < after.length ? after[j] : "";
      const decimal = c === "." && /[0-9]/.test(prev) && /[0-9]/.test(nextChar);
      const abbrev = /[A-Z]/.test(nextChar); // next word capitalized → likely "Dr. Smith"
      if (!decimal && !abbrev) {
        const t = text.slice(from, i + 1).trim();
        if (t) return { text: t, end: i + 1 };
      }
    }
    return null;
  }

  // speakSingleSentence fetches TTS for one sentence and plays it through the
  // serial queue. gen is the speech generation counter — stale sentences are
  // skipped. Kept for one-off callers; the streaming path uses enqueueSpeech.
  function speakSingleSentence(text, gen) {
    if (!text || !voiceOn) return;
    ensureAudioCtx();
    enqueueSpeech(text, gen);
  }

  // speakTTS routes every sentence through the shared playback chain (see
  // enqueueSpeech), so streaming, reply-fallback, and alert speech never overlap.
  // stopSpeech() bumps speechGen and cuts any in-flight audio before the new
  // generation is enqueued. The returned promise resolves when the chain has
  // drained, so callers (speakAlert) can restore state.
  function speakTTS(text) {
    if (!text) return Promise.resolve();
    ensureAudioCtx();
    stopSpeech();            // cut off any in-flight reply, bump generation
    var gen = speechGen;     // generation after the stop bump
    var chunks = splitSentences(text);
    if (!chunks.length) return Promise.resolve();
    chunks.forEach(function (c) { enqueueSpeech(c, gen); });
    return playChain;
  }

  // speakAlert speaks a server-initiated alert (bypass command, monitoring
  // webhook). If voice is currently off, it auto-toggles voice on, synthesizes
  // and speaks the alert, then restores the previous toggle state. It only
  // needs the audio context (no mic permission), so alerts are always heard.
  function speakAlert(text) {
    if (!text) return;
    const wasOn = voiceOn;
    const status = $("#voice-status");
    if (!wasOn) {
      ensureAudioCtx();
      voiceOn = true;
      const t = $("#voice-toggle");
      if (t) t.checked = true;
    }
    if (status) status.textContent = "speaking\u2026";
    const restore = function () {
      if (!wasOn) {
        voiceOn = false;
        const t = $("#voice-toggle");
        if (t) t.checked = false;
        stopIdle();
        if (status) status.textContent = "voice off \u2014 flip the toggle";
      } else if (status) {
        status.textContent = "say \u201cclark\u201d";
      }
    };
    const p = speakTTS(text);
    if (p && p.then) p.then(restore, restore);
    else restore();
  }

  function startRecording() {
    if (!micStream || recording) return;
    if (typeof MediaRecorder === "undefined") { toastErr("recording unsupported"); return; }
    recording = true;
    recStartAt = performance.now();
    silenceStart = 0;
    chunks = [];
    mediaRecorder = new MediaRecorder(micStream);
    mediaRecorder.ondataavailable = function (e) { if (e.data.size) chunks.push(e.data); };
    mediaRecorder.onstop = onRecordingStop;
    mediaRecorder.start();
    const status = $("#voice-status");
    if (status) status.textContent = "listening\u2026";
    vadRAF = requestAnimationFrame(vadLoop);
  }

  function vadLoop() {
    if (!recording || !analyser) return;
    const buf = new Float32Array(analyser.fftSize);
    analyser.getFloatTimeDomainData(buf);
    let sum = 0;
    for (let i = 0; i < buf.length; i++) sum += buf[i] * buf[i];
    const rms = Math.sqrt(sum / buf.length);
    const now = performance.now();
    if (rms > 0.02) {
      silenceStart = 0;
    } else if (!silenceStart) {
      silenceStart = now;
    } else if (now - silenceStart > 1500 && now - recStartAt > 600) {
      stopRecording();
      return;
    }
    vadRAF = requestAnimationFrame(vadLoop);
  }

  function stopRecording() {
    if (vadRAF) cancelAnimationFrame(vadRAF);
    vadRAF = 0;
    if (mediaRecorder && mediaRecorder.state !== "inactive") {
      mediaRecorder.stop();
    } else {
      onRecordingStop();
    }
  }

  async function onRecordingStop() {
    if (disarming) {
      disarming = false;
      recording = false;
      return;
    }
    recording = false;
    const status = $("#voice-status");
    if (status) status.textContent = "Processing, Sir\u2026";
    playClip("processing.wav").catch(function(){});
    try {
      const mime = mediaRecorder ? mediaRecorder.mimeType : "audio/webm";
      const blob = new Blob(chunks, { type: mime || "audio/webm" });
      const wav = await blobToWav(blob);
      const b64 = await bufferToBase64(wav);
      const d = await api("/web/api/stt", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ audio: b64 }),
      });
      const text = (d && d.text || "").trim();
      if (!text) {
        if (status) status.textContent = "nothing heard \u2014 say \u201cclark\u201d to retry";
        if (voiceOn) startWake();
        return;
      }
      if (status) status.textContent = "heard you \u2014 sending\u2026";
      sendVoiceText(text);
      if (voiceOn) startWake();
    } catch (e) {
      if (status) status.textContent = "transcription failed: " + (e && e.message ? e.message : e);
      if (voiceOn) startWake();
    }
  }

  function sendVoiceText(text) {
    if (!chatWs || chatWs.readyState !== WebSocket.OPEN) { toastErr("chat link offline"); return; }
    chatBusy = true;
    setTyping(true);
    sendFrame("chat", { text: text });
  }

  function blobToWav(blob) {
    return new Promise(function (resolve, reject) {
      const reader = new FileReader();
      reader.onload = function () {
        const arrayBuffer = reader.result;
        if (blob.type.indexOf("audio/webm") === -1 && blob.type.indexOf("audio/ogg") === -1) {
          resolve(arrayBuffer);
          return;
        }
        audioCtx = audioCtx || new (window.AudioContext || window.webkitAudioContext)();
        audioCtx.decodeAudioData(arrayBuffer).then(function (audio) {
          const ch = audio.numberOfChannels;
          const len = audio.length;
          const out = new ArrayBuffer(44 + len * 2);
          const v = new DataView(out);
          writeString(v, 0, "RIFF");
          v.setUint32(4, 36 + len * 2, true);
          writeString(v, 8, "WAVE");
          writeString(v, 12, "fmt ");
          v.setUint32(16, 16, true);
          v.setUint16(20, 1, true);
          v.setUint16(22, 1, true);
          v.setUint32(24, audio.sampleRate, true);
          v.setUint32(28, audio.sampleRate * 2, true);
          v.setUint16(32, 2, true);
          v.setUint16(34, 16, true);
          writeString(v, 36, "data");
          v.setUint32(40, len * 2, true);
          const data = audio.getChannelData(0);
          let off = 44;
          for (let i = 0; i < len; i++) {
            const s = Math.max(-1, Math.min(1, data[i]));
            v.setInt16(off, s < 0 ? s * 0x8000 : s * 0x7fff, true);
            off += 2;
          }
          resolve(out);
        }).catch(reject);
      };
      reader.onerror = reject;
      reader.readAsArrayBuffer(blob);
    });
  }

  function writeString(v, off, s) {
    for (let i = 0; i < s.length; i++) v.setUint8(off + i, s.charCodeAt(i));
  }

  function bufferToBase64(buf) {
    const bytes = new Uint8Array(buf);
    let bin = "";
    const CHUNK = 0x8000;
    for (let i = 0; i < bytes.length; i += CHUNK) {
      bin += String.fromCharCode.apply(null, bytes.subarray(i, i + CHUNK));
    }
    return btoa(bin);
  }

  /* ---------------- real-time sync ---------------- */

  // refreshAfterTurn pushes a fresh snapshot + history after a chat reply, so
  // a status change made through chat (voice or text) shows up immediately.
  function refreshAfterTurn() {
    pollState();
    if (mode === "bento") refreshHistory();
  }

  // pollState re-renders the bento from the live state so changes made outside
  // this tab (WhatsApp, iMessage, voice) appear without a manual refresh.
  function pollState() {
    api("/web/api/state").then(function (d) {
      if (!d || !d.state) return;
      state = d.state;
      captureState();
      renderVips();
      renderAccess();
      renderVoiceMeta();
    }).catch(function (e) {
      if (e.message === "session expired") return;
    });
  }

  /* ---------------- init ---------------- */

  setInterval(function () {
    if (chatWs && chatWs.readyState === WebSocket.OPEN) sendFrame("ping", {});
  }, 25000);

  // Safety net only — live state is pushed over the chat WebSocket the instant
  // any setting changes, so this slow poll just catches any missed push.
  setInterval(function () {
    if (token) pollState();
  }, 30000);

  setInterval(function () {
    if (!document.hidden && mode === "bento" && token) refreshHistory();
  }, 15000);

  window.addEventListener("beforeunload", function () {
    stopIdle();
    stopWake();
    if (recording && mediaRecorder) { try { mediaRecorder.stop(); } catch (e) {} }
    if (chatWs) chatWs.close();
    if (logsWs) logsWs.close();
  });

  if (token) {
    boot();
  } else {
    showLogin();
  }
})();
