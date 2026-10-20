'use strict';

// ===================== Helpers =====================

const $ = id => document.getElementById(id);
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
const safeUrl = u => /^https?:\/\//.test(u || '') ? u : '#';
const num = n => n >= 100 ? Math.round(n).toLocaleString('en-US') : String(Math.round(n * 10) / 10);
const todayISO = () => new Date().toLocaleDateString('en-CA'); // YYYY-MM-DD in the user's time zone
const shiftDate = (date, days) => { const d = new Date(date + 'T12:00'); d.setDate(d.getDate() + days); return d.toLocaleDateString('en-CA'); };

// Everything the page knows. `me` is the account from the server: username, profile, targets, nutrients.
const state = { me: null, date: todayISO(), entries: [], pendingDate: null, logVersion: 0, advice: null, history: [], last: null };

async function api(method, path, body) {
  const res = await fetch(path, { method, headers: { 'Content-Type': 'application/json' }, body: body && JSON.stringify(body) });
  if (!res.ok) {
    if (res.status === 401 && path !== '/api/login') showAuth(); // session gone, e.g. the token expired
    throw new Error((await res.text()).trim() || res.statusText);
  }
  return res.status === 204 ? null : res.json();
}

function toast(text, kind = '') {
  const el = document.createElement('div');
  el.className = `toast ${kind}`;
  el.textContent = text;
  $('toasts').append(el);
  setTimeout(() => el.remove(), 4500);
}

// Runs fn while the button shows a spinner and can't be clicked twice.
async function withBusy(button, fn) {
  button.disabled = true;
  button.classList.add('busy');
  try { return await fn(); } finally { button.disabled = false; button.classList.remove('busy'); }
}

// Status of a nutrient, as a CSS class and a label: color is never the only signal.
const status = pct => pct >= 80 ? ['good', '✓ On track'] : pct >= 50 ? ['warn', '– Below target'] : ['bad', '↓ Low'];

const meter = (label, amount, target, unit) => {
  const pct = target ? amount / target * 100 : 0;
  return `<div class="meter"><span>${esc(label)}</span>
    <div class="bar ${status(pct)[0]}" role="img" aria-label="${esc(label)}: ${num(amount)} of ${num(target)} ${esc(unit)}"><i style="width:${Math.min(100, pct)}%"></i></div>
    <em>${num(amount)} / ${num(target)} ${esc(unit)}</em></div>`;
};

const dayLabel = date => date === todayISO() ? 'Today' : date === shiftDate(todayISO(), -1) ? 'Yesterday'
  : new Date(date + 'T12:00').toLocaleDateString('en-US', { weekday: 'long', month: 'short', day: 'numeric' });

// ===================== Account =====================

let authMode = 'login';

function showAuth() {
  state.me = null;
  $('app').hidden = $('user').hidden = $('tabs').hidden = true;
  $('auth').hidden = false;
  $('username').focus();
}

function setAccount(me) {
  state.me = me;
  $('auth').hidden = true;
  $('app').hidden = $('user').hidden = $('tabs').hidden = false;
  $('uname').textContent = me.username;
  $('avatar').textContent = me.username[0];
  route();
}

$('auth-form').addEventListener('submit', e => {
  e.preventDefault();
  $('auth-error').textContent = '';
  withBusy($('auth-btn'), async () => {
    try {
      setAccount(await api('POST', `/api/${authMode}`, { username: $('username').value.trim(), password: $('password').value }));
      $('password').value = '';
    } catch (err) {
      $('auth-error').textContent = err.message;
    }
  });
});

$('switch').addEventListener('click', () => {
  authMode = authMode === 'login' ? 'register' : 'login';
  const reg = authMode === 'register';
  $('auth-title').textContent = reg ? 'Create your account' : 'Welcome back';
  $('auth-sub').textContent = reg ? 'Step 1 of 2. Next, you tell us about yourself.' : 'Log in to see your meal plans, food log and insights.';
  $('auth-btn').textContent = reg ? 'Create account' : 'Log in';
  $('switch-text').textContent = reg ? 'Already have an account?' : 'New here?';
  $('switch').textContent = reg ? 'Log in' : 'Create an account';
  $('password').autocomplete = reg ? 'new-password' : 'current-password';
  $('auth-error').textContent = '';
});

$('logout').addEventListener('click', async () => {
  await api('POST', '/api/logout').catch(() => {});
  location.hash = '';
  location.reload(); // simplest way to forget everything about the previous user
});

// ===================== Navigation =====================

const VIEWS = { plan: showPlan, log: loadLog, insights: loadInsights, profile: showProfile };

// The URL hash picks the view (#plan, #log, ...), so refresh and the back button work.
function route() {
  if (!state.me) return;
  const onboarding = !state.me.profile; // a new account must fill in the profile first
  document.body.classList.toggle('onboarding', onboarding);
  const wanted = location.hash.slice(1);
  const view = onboarding ? 'profile' : wanted in VIEWS ? wanted : 'plan';
  for (const name in VIEWS) $(`view-${name}`).hidden = name !== view;
  document.querySelectorAll('#tabs a').forEach(a => a.getAttribute('href') === '#' + view ? a.setAttribute('aria-current', 'page') : a.removeAttribute('aria-current'));
  VIEWS[view]();
}
window.addEventListener('hashchange', route);

// ===================== Profile & onboarding =====================

const form = $('profile');

function readProfile() {
  const f = new FormData(form);
  return { sex: f.get('sex'), age: +f.get('age'), weight: +f.get('weight'), height: +f.get('height'), activity: f.get('activity'),
    goal: f.get('goal'), diet: f.get('diet'), intolerances: f.getAll('int'), notes: f.get('notes').trim() };
}

function fillProfile(p) {
  const el = form.elements;
  for (const k of ['sex', 'age', 'weight', 'height', 'activity', 'goal', 'diet', 'notes']) el[k].value = p[k];
  form.querySelectorAll('[name=int]').forEach(c => c.checked = (p.intolerances || []).includes(c.value));
}

function showProfile() {
  const first = !state.me.profile;
  $('onb-badge').hidden = !first;
  $('profile-title').textContent = first ? `Welcome, ${state.me.username}! Tell us about you` : 'Your profile';
  $('profile-sub').textContent = first
    ? 'We use this to calculate your calories and the vitamins and minerals you need every day.'
    : 'Changes update your daily targets, meal plans and insights.';
  $('profile-btn').textContent = first ? 'Continue' : 'Save profile';
  if (!first) fillProfile(state.me.profile);
  renderTargets();
}

function renderTargets() {
  const { profile, targets: t, nutrients } = state.me;
  if (!profile) {
    $('targets-card').innerHTML = `<h3>Your daily targets</h3><p class="muted">Fill in your details and we will calculate your calories and the nutrients you need.</p>`;
    return;
  }
  $('targets-card').innerHTML = `
    <h3>Your daily targets</h3>
    <div class="big">${num(t.calories)} <small class="muted">kcal</small></div>
    <p class="muted">Meal plan macros: protein ${t.protein_g} g · carbs ${t.carbs_g} g · fat ${t.fat_g} g<br>BMR ${num(t.bmr)} kcal · BMI ${t.bmi}</p>
    <b>Daily minimums</b>
    <ul>${nutrients.map(n => `<li><span>${esc(n.label)}</span><b>${num(n.target)} ${esc(n.unit)}</b></li>`).join('')}</ul>
    <p class="source">Calories: Mifflin-St Jeor equation. Vitamins and minerals:
      <a href="https://ods.od.nih.gov/HealthInformation/nutrientrecommendations.aspx" target="_blank" rel="noopener">NIH Dietary Reference Intakes</a> for your age and sex.</p>`;
}

form.addEventListener('submit', e => {
  e.preventDefault();
  const first = !state.me.profile;
  withBusy($('profile-btn'), async () => {
    try {
      state.me = await api('PUT', '/api/profile', readProfile());
      state.advice = null; // targets changed, so old advice is stale
      if (first) {
        toast('All set! Start by logging what you ate today.');
        history.replaceState(null, '', '#log');
        route();
      } else {
        toast('Profile saved ✓');
        renderTargets();
      }
    } catch (err) {
      toast(err.message, 'error');
    }
  });
});

// ===================== Meal plan (the recipe agent) =====================

const toolNames = { calculate_targets: 'Calculate targets', search_recipes: 'Search recipes', review_plan: 'Check plan' };

function showPlan() {
  const { profile: p, targets: t } = state.me;
  $('hero-kcal').textContent = num(t.calories);
  $('hero-macros').textContent = `kcal · P ${t.protein_g} g · C ${t.carbs_g} g · F ${t.fat_g} g`;
  const goal = { lose: 'Losing weight', maintain: 'Maintaining', gain: 'Gaining' }[p.goal];
  const tags = [`${p.sex === 'male' ? 'Male' : 'Female'}, ${p.age}`, `${p.weight} kg · ${p.height} cm`, goal, p.diet || 'No diet restrictions'];
  $('hero-who').innerHTML = tags.map(x => `<span class="tag">${esc(x)}</span>`).join('')
    + (p.intolerances || []).map(i => `<span class="tag warm">No ${esc(i)}</span>`).join('')
    + ' <a class="link" href="#profile">Edit</a>';
}

$('plan-form').addEventListener('submit', e => {
  e.preventDefault();
  const p = state.me.profile;
  const ints = p.intolerances || [];
  const prefs = [p.notes, $('plan-extra').value.trim()].filter(Boolean).join('. ');
  const text = `My profile: sex=${p.sex}, age=${p.age}, weight=${p.weight} kg, height=${p.height} cm, activity=${p.activity}, goal=${p.goal}. ` +
    `Diet: ${p.diet || 'no restrictions'}. Intolerances: ${ints.length ? ints.join(', ') : 'none'}. ` +
    `Preferences: ${prefs || 'none'}. Make me a plan for one day.`;
  state.history = [];
  sendPlan(text);
});

async function sendPlan(text) {
  const msgs = [...state.history, { role: 'user', content: text }];
  const buttons = document.querySelectorAll('#view-plan button');
  buttons.forEach(b => b.disabled = true);
  showPlanLoading();
  try {
    const { plan, steps } = await api('POST', '/api/chat', { messages: msgs });
    const memo = { message: plan.message, targets: plan.targets, meals: plan.meals.map(m => ({ meal: m.meal, recipe_id: m.recipe_id, title: m.recipe.title })) };
    state.history = [...msgs, { role: 'assistant', content: JSON.stringify(memo) }];
    state.last = [plan, steps];
    renderPlan(plan, steps);
  } catch (err) {
    if (state.last) renderPlan(...state.last); else $('out').innerHTML = '';
    $('out').insertAdjacentHTML('afterbegin', `<div class="panel error" role="alert">Something went wrong: ${esc(err.message)}</div>`);
  } finally {
    document.querySelectorAll('#view-plan button').forEach(b => b.disabled = false);
  }
}

function showPlanLoading() {
  const phrases = ['Calculating your calorie needs…', 'Searching for matching recipes…', 'Balancing the meals…', 'Putting the plan together…'];
  $('out').innerHTML = `<div class="panel loading"><div class="spinner"></div><div><b>The agent is working</b><div id="phrase" class="muted">${phrases[0]}</div></div></div><div class="skeleton" style="height:300px"></div>`;
  let i = 0;
  const timer = setInterval(() => {
    const el = $('phrase');
    if (!el) return clearInterval(timer);
    el.textContent = phrases[++i % phrases.length];
  }, 2500);
}

function renderPlan(plan, steps) {
  const t = plan.targets;
  const sum = k => plan.meals.reduce((s, m) => s + m.recipe[k], 0);
  const macro = (name, key, target, color) => {
    const got = sum(key);
    return `<div class="macro"><b>${name}</b><div class="bar"><i style="width:${Math.min(100, got / (target || 1) * 100)}%;background:${color}"></i></div><em>${got} / ${target} g</em></div>`;
  };
  const meals = plan.meals.map(m => {
    const r = m.recipe;
    return `<article class="panel meal">
      <figure><img src="${esc(safeUrl(r.image))}" alt="${esc(r.title)}" loading="lazy"><span class="badge">${esc(m.meal)}</span></figure>
      <div class="body">
        <h3>${esc(r.title)}</h3>
        <div class="meta"><span class="k">${r.calories} kcal</span><span>P ${r.protein_g}g</span><span>C ${r.carbs_g}g</span><span>F ${r.fat_g}g</span>${r.ready_in_minutes ? `<span>${r.ready_in_minutes} min</span>` : ''}</div>
        <p class="why">${esc(m.why)}</p>
        <a href="${esc(safeUrl(r.url))}" target="_blank" rel="noopener">View recipe →</a>
      </div></article>`;
  }).join('');
  const stepList = steps.map(s => `<li><b>${esc(toolNames[s.tool] || s.tool)}</b><code>${esc(prettyArgs(s.args))}</code></li>`).join('');

  $('out').innerHTML = `
    <div class="panel summary">
      <div class="kcal"><small>Daily target</small><strong>${t.calories}</strong><span>kcal · plan has ${sum('calories')}</span></div>
      <div><p>${esc(plan.message)}</p>
        ${macro('Protein', 'protein_g', t.protein_g, 'var(--protein)')}
        ${macro('Carbs', 'carbs_g', t.carbs_g, 'var(--carbs)')}
        ${macro('Fat', 'fat_g', t.fat_g, 'var(--fat)')}
      </div>
    </div>
    ${meals ? `<div class="meals">${meals}</div>` : '<div class="panel error">The agent found no matching recipes. Try relaxing your filters.</div>'}
    <form class="panel ask" id="ask">
      <label for="q" class="sr">Ask for a change</label>
      <input id="q" type="text" placeholder="Ask for a change, e.g. “swap dinner for something with fish”" required maxlength="500">
      <button class="btn">Send</button>
    </form>
    ${steps.length ? `<details class="panel"><summary>What the agent did · ${steps.length} tool calls</summary><ol class="steps">${stepList}</ol></details>` : ''}`;
  $('ask').addEventListener('submit', e => { e.preventDefault(); sendPlan($('q').value.trim()); });
}

function prettyArgs(args) {
  try { return Object.entries(JSON.parse(args)).map(([k, v]) => `${k}: ${v}`).join(' · '); } catch { return args; }
}

// ===================== Food log =====================

const EXAMPLES = ['2 scrambled eggs, toast and orange juice', 'Chicken caesar salad', 'A banana and a handful of almonds', 'Spaghetti bolognese and a glass of red wine'];
$('examples').innerHTML = EXAMPLES.map(x => `<button type="button">${esc(x)}</button>`).join('');
$('examples').addEventListener('click', e => {
  if (e.target.tagName !== 'BUTTON') return;
  $('log-text').value = e.target.textContent;
  $('log-text').focus();
});
$('log-text').addEventListener('keydown', e => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) $('log-form').requestSubmit(); });

async function loadLog() {
  const date = state.date;
  $('log-date').value = date;
  $('log-date').max = todayISO();
  $('day-next').disabled = date >= todayISO();
  $('entries').innerHTML = '<div class="skeleton" style="height:140px"></div>';
  try {
    const { entries } = await api('GET', `/api/log?date=${date}`);
    if (date !== state.date) return; // the user already moved to another day
    state.entries = entries;
    renderLog();
  } catch (err) {
    $('entries').innerHTML = `<div class="panel error">${esc(err.message)}</div>`;
  }
}

function setDate(date) {
  if (!date || date > todayISO()) return;
  state.date = date;
  loadLog();
}
$('log-date').addEventListener('change', e => setDate(e.target.value));
$('day-prev').addEventListener('click', () => setDate(shiftDate(state.date, -1)));
$('day-next').addEventListener('click', () => setDate(shiftDate(state.date, 1)));

function renderLog() {
  const total = {};
  for (const e of state.entries) for (const it of e.items) for (const [k, v] of Object.entries(it.nutrients)) total[k] = (total[k] || 0) + v;
  const kcalTarget = state.me.targets.calories;
  $('day-card').innerHTML = `
    <h3>${esc(dayLabel(state.date))}</h3>
    <div class="day-kcal"><strong>${num(total.kcal || 0)}</strong><span class="muted">/ ${num(kcalTarget)} kcal</span></div>
    <div class="bar" role="img" aria-label="Calories: ${num(total.kcal || 0)} of ${num(kcalTarget)}"><i style="width:${Math.min(100, (total.kcal || 0) / kcalTarget * 100)}%;background:var(--accent)"></i></div>
    <div class="meters">${state.me.nutrients.map(n => meter(n.label, total[n.key] || 0, n.target, n.unit)).join('')}</div>`;
  const pending = state.pendingDate === state.date ? `<div class="panel entry pending"><div class="spinner"></div>
    <div><b>Analyzing your meal…</b><div class="muted">Splitting it into foods and matching each one in the USDA database.</div></div></div>` : '';
  $('entries').innerHTML = pending + (state.entries.length || pending
    ? state.entries.slice().reverse().map(entryCard).join('')
    : `<div class="panel empty"><h3>Nothing logged ${state.date === todayISO() ? 'today' : 'on this day'}</h3><p>Write what you ate, as you would tell a friend. Amounts are optional; we estimate typical portions.</p></div>`);
}

function entryCard(e) {
  const kcal = e.items.reduce((s, it) => s + (it.nutrients.kcal || 0), 0);
  const rows = e.items.map(it => `<tr>
      <td>${esc(it.name)}${it.match ? `<small>USDA: ${esc(it.match)}</small>` : '<small class="nomatch">No USDA match, not counted</small>'}</td>
      <td class="num">${num(it.grams)} g</td>
      <td class="num">${it.match ? `${num(it.nutrients.kcal || 0)} kcal` : '–'}</td></tr>`).join('');
  return `<article class="panel entry">
    <div class="entry-head"><q>${esc(e.text)}</q>
      <button class="icon-btn" data-delete="${esc(e.id)}" aria-label="Delete this entry"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6"/></svg></button>
    </div>
    <table><tbody>${rows}</tbody><tfoot><tr><td>Total</td><td></td><td class="num">${num(kcal)} kcal</td></tr></tfoot></table>
  </article>`;
}

$('log-form').addEventListener('submit', e => {
  e.preventDefault();
  const text = $('log-text').value.trim();
  const date = state.date;
  if (!text) return;
  state.pendingDate = date; // part of the state, so any re-render keeps the "Analyzing" card
  renderLog();
  withBusy($('log-btn'), async () => {
    try {
      const entry = await api('POST', '/api/log', { date, text });
      $('log-text').value = '';
      state.logVersion++;
      if (date === state.date) state.entries.push(entry);
      const missed = entry.items.filter(i => !i.match).length;
      toast(missed ? `Added. ${missed} food${missed > 1 ? 's' : ''} had no USDA match and ${missed > 1 ? 'are' : 'is'} not counted.` : `Added ${entry.items.length} food${entry.items.length > 1 ? 's' : ''} ✓`);
    } catch (err) {
      toast(err.message, 'error');
    } finally {
      state.pendingDate = null;
      if (date === state.date) renderLog();
    }
  });
});

$('entries').addEventListener('click', async e => {
  const btn = e.target.closest('[data-delete]');
  if (!btn) return;
  const card = btn.closest('.entry');
  card.style.opacity = '.4';
  try {
    await api('DELETE', `/api/log/${state.date}/${btn.dataset.delete}`);
    state.entries = state.entries.filter(x => x.id !== btn.dataset.delete);
    state.logVersion++;
    renderLog();
    toast('Entry deleted');
  } catch (err) {
    card.style.opacity = '';
    toast(err.message, 'error');
  }
});

// ===================== Insights =====================

async function loadInsights() {
  $('ins-body').innerHTML = `<div class="skeleton" style="height:96px;margin-bottom:20px"></div><div class="nutrient-grid">${'<div class="skeleton" style="height:170px"></div>'.repeat(8)}</div>`;
  try {
    const ins = await api('GET', '/api/insights');
    renderInsights(ins);
    if (ins.days) loadAdvice();
  } catch (err) {
    $('ins-body').innerHTML = `<div class="panel error">${esc(err.message)}</div>`;
  }
}

function renderInsights(ins) {
  $('ins-sub').textContent = ins.days
    ? `Last 7 days · ${ins.days} day${ins.days > 1 ? 's' : ''} logged · ${num(ins.kcal)} kcal per day on average`
    : 'Last 7 days';
  if (!ins.days) {
    $('ins-body').innerHTML = `<div class="panel empty">
      <svg width="56" height="56" viewBox="0 0 24 24" fill="none" stroke="var(--accent)" stroke-width="1.5" stroke-linecap="round" aria-hidden="true"><path d="M3 3v18h18M7 16l4-6 4 3 5-7"/></svg>
      <h3>No meals logged in the last 7 days</h3>
      <p>Log what you eat for a few days and we will show which vitamins and minerals you are missing, and what to eat to get them.</p>
      <a class="btn" href="#log">Go to the food log</a></div>`;
    return;
  }
  const sorted = [...ins.nutrients].sort((a, b) => a.percent - b.percent); // biggest gaps first
  $('ins-body').innerHTML = `
    <div class="panel ai-card" id="ai-card">
      <div class="ai-icon" aria-hidden="true"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9z"/></svg></div>
      <div><h3>What we noticed</h3><div class="skeleton line" style="width:90%"></div><div class="skeleton line" style="width:70%"></div></div>
    </div>
    <div class="nutrient-grid">${sorted.map(nutrientCard).join('')}</div>
    <p class="source">Targets: NIH Dietary Reference Intakes for your age and sex. Nutrient values: USDA FoodData Central.
      Averages count only the days you logged. General information, not medical advice.</p>`;
}

function nutrientCard(n) {
  const [cls, label] = status(n.percent);
  return `<article class="panel ncard" id="n-${n.key}">
    <div class="ncard-head"><h3>${esc(n.label)}</h3><span class="pill ${cls}">${label}</span></div>
    <div class="pct">${n.percent}%<small>of daily target</small></div>
    <div class="bar ${cls}" role="img" aria-label="${esc(n.label)}: ${n.percent}% of the daily target"><i style="width:${Math.min(100, n.percent)}%"></i></div>
    <p class="tip">${num(n.amount)} ${esc(n.unit)} of ${num(n.target)} ${esc(n.unit)} per day</p>
    <div class="tip-slot"></div>
    ${n.percent < 80 ? `<button class="btn ghost small" data-recipes="${n.key}">Find recipes rich in ${esc(n.label)}</button><div class="minis"></div>` : ''}
  </article>`;
}

async function loadAdvice() {
  try {
    if (state.advice?.version !== state.logVersion) { // reuse the advice until the log changes
      state.advice = { ...(await api('GET', '/api/insights/advice')), version: state.logVersion };
    }
    const card = $('ai-card');
    if (!card) return; // the user left the insights view
    card.lastElementChild.innerHTML = `<h3>What we noticed</h3><p>${esc(state.advice.summary)}</p>`;
    for (const t of state.advice.tips) {
      const slot = document.querySelector(`#n-${t.nutrient} .tip-slot`);
      if (slot) slot.innerHTML = `<p class="tip">${esc(t.advice)}</p>${t.foods.length ? `<div class="foods">${t.foods.map(f => `<span>${esc(f)}</span>`).join('')}</div>` : ''}`;
    }
  } catch (err) {
    const card = $('ai-card');
    if (card) card.lastElementChild.innerHTML = `<h3>What we noticed</h3><p class="muted">The AI summary is not available right now (${esc(err.message)}). <button class="link" id="ai-retry">Try again</button></p>`;
  }
}

$('ins-body').addEventListener('click', e => {
  if (e.target.id === 'ai-retry') return loadAdvice();
  const btn = e.target.closest('[data-recipes]');
  if (!btn) return;
  const n = state.me.nutrients.find(x => x.key === btn.dataset.recipes);
  const box = btn.nextElementSibling;
  withBusy(btn, async () => {
    try {
      const { recipes } = await api('GET', `/api/recipes?nutrient=${n.key}`);
      box.innerHTML = recipes.length
        ? recipes.map(r => `<div class="mini"><img src="${esc(safeUrl(r.image))}" alt="" loading="lazy">
            <div><a href="${esc(safeUrl(r.url))}" target="_blank" rel="noopener">${esc(r.title)}</a><span class="muted">${num(r.amount)} ${esc(n.unit)} per serving · ${r.calories} kcal</span></div></div>`).join('')
        : '<p class="tip">No recipes found that also fit your diet and allergies. The foods above are a good start.</p>';
      btn.hidden = true;
    } catch (err) {
      toast(err.message, 'error');
    }
  });
});

// ===================== Start =====================

api('GET', '/api/me').then(setAccount).catch(() => {});
