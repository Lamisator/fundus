// Interactive API reference rendered from /api/openapi.json.
import { html, raw, icon, mount, api, $, $$, on, esc, copy } from '../util.js';
import { setCrumbs, setTitle } from '../app.js';

export default async function ({ el }) {
  setTitle('API documentation');
  setCrumbs([{ label: 'API documentation' }], html`<a class="btn sm" href="/api/openapi.yaml" target="_blank">${icon('download', 'sm')}openapi.yaml</a><a class="btn sm" href="/api/openapi.json" target="_blank">JSON</a>`);
  const res = await fetch('/api/openapi.json');
  if (!res.ok) throw new Error('Could not load the OpenAPI document (HTTP ' + res.status + ').');
  const spec = await res.json();
  const base = location.origin;

  const resolve = (s, depth = 0) => {
    if (!s || depth > 8) return s || {};
    if (s.$ref) return resolve(spec.components.schemas[s.$ref.split('/').pop()], depth + 1);
    return s;
  };
  const refName = (s) => (s?.$ref ? s.$ref.split('/').pop() : '');

  const schemaHTML = (s, depth = 0, seen = new Set()) => {
    const name = refName(s);
    s = resolve(s);
    if (name && seen.has(name)) return html`<span class="ty">${name}</span>`;
    if (name) seen = new Set([...seen, name]);
    if (s.type === 'array') return html`<span class="ty">array of</span> ${schemaHTML(s.items, depth, seen)}`;
    if (s.allOf) return html`${s.allOf.map((x) => schemaHTML(x, depth, seen))}`;
    if (s.type === 'object' || s.properties) {
      if (!s.properties) return html`<span class="ty">object</span>${s.additionalProperties ? html` <span class="ds">(map of ${resolve(s.additionalProperties).type || 'any'})</span>` : ''}`;
      if (depth > 2) return html`<span class="ty">${name || 'object'}</span>`;
      const req = new Set(s.required || []);
      return html`<span class="ty">${name || 'object'}</span> {<div style="padding-left:18px">${Object.entries(s.properties).map(([k, v]) => {
        const r = resolve(v);
        return html`<div><span class="p">${k}</span>${req.has(k) ? html`<b style="color:var(--danger)">*</b>` : ''}: ${['object', 'array'].includes(r.type) || r.properties || v.$ref ? schemaHTML(v, depth + 1, seen) : html`<span class="ty">${r.type || 'any'}${r.format ? ` (${r.format})` : ''}${r.enum ? ' ' + r.enum.join(' | ') : ''}${r.nullable ? ' | null' : ''}</span>`}${r.description || v.description ? html` <span class="ds">— ${v.description || r.description}</span>` : ''}</div>`;
      })}</div>}`;
    }
    return html`<span class="ty">${s.type || 'any'}${s.format ? ` (${s.format})` : ''}${s.enum ? ' ' + s.enum.join(' | ') : ''}</span>`;
  };

  const example = (s, depth = 0) => {
    s = resolve(s);
    if (s.example !== undefined) return s.example;
    if (depth > 4) return null;
    if (s.type === 'array') return [example(s.items, depth + 1)];
    if (s.type === 'object' || s.properties) {
      const o = {};
      for (const [k, v] of Object.entries(s.properties || {})) { const ex = example(v, depth + 1); if (ex !== null && ex !== undefined) o[k] = ex; }
      return o;
    }
    return { string: 'string', integer: 0, boolean: false, number: 0 }[s.type] ?? null;
  };

  const ops = [];
  for (const [path, item] of Object.entries(spec.paths)) {
    for (const m of ['get', 'post', 'put', 'patch', 'delete']) {
      if (!item[m]) continue;
      const op = item[m];
      ops.push({ path, method: m, op, tag: (op.tags || ['Other'])[0], id: (m + path).replace(/[^a-z0-9]+/gi, '-'), params: [...(item.parameters || []), ...(op.parameters || [])] });
    }
  }
  const tags = (spec.tags || []).map((t) => t.name).filter((n) => ops.some((o) => o.tag === n));
  ops.forEach((o) => { if (!tags.includes(o.tag)) tags.push(o.tag); });

  const curl = (o) => {
    const p = o.path.replace(/\{(\w+)\}/g, (_, n) => (n === 'id' ? '42' : `<${n}>`));
    let c = `curl ${o.method === 'get' ? '' : '-X ' + o.method.toUpperCase() + ' '}"${base}${p}"`;
    if (o.op.security?.length === 0) return c;
    c += ' \\\n  -H "Authorization: Bearer $FUNDUS_TOKEN"';
    const body = o.op.requestBody?.content?.['application/json'];
    if (body) c += ` \\\n  -H "Content-Type: application/json" \\\n  -d '${JSON.stringify(body.example || example(body.schema), null, 0)}'`;
    if (o.op.requestBody?.content?.['multipart/form-data']) c += ' \\\n  -F "file=@screenshot.png" -F "entry_id=42"';
    return c;
  };

  mount(el, html`
    <div class="page-head"><div class="grow"><h1>${spec.info.title}</h1><p>Version ${spec.info.version} · OpenAPI ${spec.openapi} · base URL <code>${base}/api/v1</code></p></div></div>
    <div class="api-layout">
      <nav class="api-nav">
        <h4>Guide</h4><a href="#intro">Introduction</a><a href="#auth">Authentication</a><a href="#errors">Errors & versions</a><a href="#mcp">MCP for AI assistants</a>
        ${tags.map((t) => html`<h4>${t}</h4>${ops.filter((o) => o.tag === t).map((o) => html`<a href="#${o.id}"><span class="method m-${o.method}">${o.method.toUpperCase()}</span><span class="ellipsis">${o.op.summary || o.path}</span></a>`)}`)}
      </nav>
      <div>
        <div class="card card-pad prose" id="intro" style="margin-bottom:18px">${raw(mdLite(spec.info.description || ''))}</div>
        ${tags.map((t) => html`<h2 style="font-family:var(--font-display);font-weight:600;margin:28px 0 12px">${t}</h2>
          ${(spec.tags.find((x) => x.name === t)?.description) ? html`<p class="muted" style="margin-top:-6px">${spec.tags.find((x) => x.name === t).description}</p>` : ''}
          ${ops.filter((o) => o.tag === t).map((o) => html`<div class="card op" id="${o.id}">
            <div class="op-head" data-toggle><span class="method m-${o.method}">${o.method.toUpperCase()}</span><code>${o.path}</code><span class="sum">${o.op.summary || ''}</span></div>
            <div class="op-body hidden">
              ${o.op.description ? html`<div class="prose" style="font-size:14px;margin-top:12px">${raw(mdLite(o.op.description))}</div>` : ''}
              ${o.op.security?.length === 0 ? html`<p class="small" style="color:var(--success)">No authentication required.</p>` : ''}
              ${o.params.length ? html`<h5>Parameters</h5><table class="table params"><tbody>${o.params.map((p) => {
                p = p.$ref ? spec.components.parameters[p.$ref.split('/').pop()] : p;
                const s = resolve(p.schema);
                return html`<tr><td>${p.name}${p.required ? html`<b style="color:var(--danger)">*</b>` : ''}</td><td class="small faint">${p.in}</td><td class="small"><span class="mono" style="color:var(--success)">${s.type === 'array' ? 'array' : s.type}${s.enum ? ': ' + s.enum.join(' | ') : ''}</span> ${p.description || ''}</td></tr>`;
              })}</tbody></table>` : ''}
              ${o.op.requestBody ? html`<h5>Request body</h5>${Object.entries(o.op.requestBody.content).map(([ct, c]) => html`<div class="small faint" style="margin-bottom:4px">${ct}</div><div class="schema code-box">${schemaHTML(c.schema)}</div>`)}` : ''}
              <h5>Responses</h5>${Object.entries(o.op.responses || {}).map(([code, r]) => {
                const c = r.content && Object.values(r.content)[0];
                return html`<div style="margin-bottom:8px"><b class="mono" style="color:${code < 300 ? 'var(--success)' : 'var(--danger)'}">${code}</b> <span class="muted">${r.description || ''}</span>
                  ${c?.schema && code < 300 ? html`<div class="schema code-box" style="margin-top:6px">${schemaHTML(c.schema)}</div>` : ''}</div>`;
              })}
              <h5>Example</h5><div class="code-box" style="padding-right:70px">${curl(o)}<button class="btn sm copy" style="position:absolute;top:7px;right:7px" data-curl="${o.id}">${icon('copy', 'sm')}</button></div>
              ${o.method === 'get' && !o.path.includes('{') ? html`<div class="try"><button class="btn sm" data-try="${o.path}">${icon('bolt', 'sm')}Try it with your session</button><pre class="code-box hidden"></pre></div>` : ''}
            </div>
          </div>`)}`)}
      </div>
    </div>`);

  on(el, 'click', '[data-toggle]', (e, h) => h.nextElementSibling.classList.toggle('hidden'));
  on(el, 'click', '[data-curl]', (e, b) => copy(curl(ops.find((o) => o.id === b.dataset.curl)), 'curl command copied'));
  on(el, 'click', '[data-try]', async (e, b) => {
    const pre = b.nextElementSibling;
    pre.classList.remove('hidden');
    pre.textContent = 'Loading…';
    try {
      const r = await fetch(b.dataset.try + (b.dataset.try.endsWith('entries') || b.dataset.try.endsWith('search') ? '?limit=2' : ''), { headers: { 'X-Requested-With': 'fundus' } });
      const t = await r.text();
      try { pre.textContent = `HTTP ${r.status}\n\n` + JSON.stringify(JSON.parse(t), null, 2); } catch { pre.textContent = t; }
    } catch (err) { pre.textContent = err.message; }
  });
  on(el, 'click', '.api-nav a', (e, a) => {
    const t = document.getElementById(a.getAttribute('href').slice(1));
    if (!t) return;
    e.preventDefault();
    t.querySelector('.op-body')?.classList.remove('hidden');
    t.scrollIntoView({ behavior: 'smooth' });
  });
}

// Tiny Markdown subset for spec descriptions: headings, code, lists, links, paragraphs.
function mdLite(src) {
  const out = [];
  const lines = src.split('\n');
  let i = 0;
  const inline = (s) => esc(s).replace(/`([^`]+)`/g, '<code>$1</code>').replace(/\*\*([^*]+)\*\*/g, '<b>$1</b>')
    .replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, '<a href="$2">$1</a>');
  while (i < lines.length) {
    const l = lines[i];
    if (l.startsWith('```')) {
      const buf = [];
      i++;
      while (i < lines.length && !lines[i].startsWith('```')) buf.push(lines[i++]);
      i++;
      out.push(`<pre><code>${esc(buf.join('\n'))}</code></pre>`);
    } else if (/^#{1,4} /.test(l)) {
      const n = l.match(/^#+/)[0].length;
      const text = l.slice(n + 1);
      const id = { 'Authentication': 'auth', 'Errors and versions': 'errors', 'MCP for AI assistants': 'mcp' }[text] || '';
      out.push(`<h${n + 1}${id ? ` id="${id}"` : ''}>${inline(text)}</h${n + 1}>`);
      i++;
    } else if (/^[-*] /.test(l)) {
      const buf = [];
      while (i < lines.length && /^[-*] /.test(lines[i])) buf.push(`<li>${inline(lines[i++].slice(2))}</li>`);
      out.push(`<ul>${buf.join('')}</ul>`);
    } else if (/^\|/.test(l)) {
      const rows = [];
      while (i < lines.length && /^\|/.test(lines[i])) rows.push(lines[i++]);
      const cells = (r) => r.split('|').slice(1, -1).map((c) => c.trim());
      out.push(`<table><thead><tr>${cells(rows[0]).map((c) => `<th>${inline(c)}</th>`).join('')}</tr></thead><tbody>${rows.slice(2).map((r) => `<tr>${cells(r).map((c) => `<td>${inline(c)}</td>`).join('')}</tr>`).join('')}</tbody></table>`);
    } else if (l.trim()) {
      const buf = [];
      while (i < lines.length && lines[i].trim() && !/^(#|```|[-*] |\|)/.test(lines[i])) buf.push(lines[i++]);
      out.push(`<p>${inline(buf.join(' '))}</p>`);
    } else i++;
  }
  return out.join('\n');
}
