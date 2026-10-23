// Minimal LCS diff for lines and words.

function lcsOps(a, b) {
  // trim common prefix / suffix to keep the table small
  let pre = 0;
  while (pre < a.length && pre < b.length && a[pre] === b[pre]) pre++;
  let suf = 0;
  while (suf < a.length - pre && suf < b.length - pre && a[a.length - 1 - suf] === b[b.length - 1 - suf]) suf++;
  const A = a.slice(pre, a.length - suf), B = b.slice(pre, b.length - suf);
  const ops = [];
  for (let i = 0; i < pre; i++) ops.push(['=', a[i]]);
  if (A.length * B.length > 4e6) {
    A.forEach((x) => ops.push(['-', x]));
    B.forEach((x) => ops.push(['+', x]));
  } else {
    const n = A.length, m = B.length;
    const dp = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
    for (let i = n - 1; i >= 0; i--)
      for (let j = m - 1; j >= 0; j--)
        dp[i][j] = A[i] === B[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    let i = 0, j = 0;
    while (i < n && j < m) {
      if (A[i] === B[j]) { ops.push(['=', A[i]]); i++; j++; }
      else if (dp[i + 1][j] >= dp[i][j + 1]) ops.push(['-', A[i++]]);
      else ops.push(['+', B[j++]]);
    }
    while (i < n) ops.push(['-', A[i++]]);
    while (j < m) ops.push(['+', B[j++]]);
  }
  for (let i = a.length - suf; i < a.length; i++) ops.push(['=', a[i]]);
  return ops;
}

const lines = (s) => (s === '' ? [] : s.split('\n'));
export const diffLines = (a, b) => lcsOps(lines(a), lines(b));

const esc = (s) => s.replace(/[&<>]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' }[c]));

function wordDiff(a, b) {
  const ta = a.split(/(\s+|\b)/), tb = b.split(/(\s+|\b)/);
  const ops = lcsOps(ta, tb);
  let l = '', r = '';
  for (const [op, t] of ops) {
    if (op === '=') { l += esc(t); r += esc(t); }
    else if (op === '-') l += `<del>${esc(t)}</del>`;
    else r += `<ins>${esc(t)}</ins>`;
  }
  return [l, r];
}

/** Render a unified line diff as HTML, folding long unchanged runs. */
export function renderDiff(a, b, context = 3) {
  const ops = diffLines(a, b);
  if (!ops.some(([o]) => o !== '=')) return '<div class="gap">No changes</div>';
  // pair up adjacent -/+ runs for word highlighting
  const rows = [];
  for (let i = 0; i < ops.length; i++) {
    if (ops[i][0] === '-') {
      const dels = [];
      while (i < ops.length && ops[i][0] === '-') dels.push(ops[i++][1]);
      const adds = [];
      while (i < ops.length && ops[i][0] === '+') adds.push(ops[i++][1]);
      i--;
      const pairs = Math.min(dels.length, adds.length);
      const dl = [], al = [];
      for (let k = 0; k < pairs; k++) { const [l, r] = wordDiff(dels[k], adds[k]); dl.push(l); al.push(r); }
      dels.slice(pairs).forEach((d) => dl.push(esc(d)));
      adds.slice(pairs).forEach((x) => al.push(esc(x)));
      dl.forEach((h) => rows.push(['-', h]));
      al.forEach((h) => rows.push(['+', h]));
    } else rows.push([ops[i][0], esc(ops[i][1])]);
  }
  const keep = rows.map(() => false);
  rows.forEach(([o], i) => { if (o !== '=') for (let k = Math.max(0, i - context); k <= Math.min(rows.length - 1, i + context); k++) keep[k] = true; });
  let out = '', skipped = 0;
  rows.forEach(([o, h], i) => {
    if (!keep[i]) { skipped++; return; }
    if (skipped) { out += `<div class="gap">⋯ ${skipped} unchanged line${skipped === 1 ? '' : 's'}</div>`; skipped = 0; }
    out += `<div class="${o === '+' ? 'add' : o === '-' ? 'del' : ''}">${h || ' '}</div>`;
  });
  if (skipped) out += `<div class="gap">⋯ ${skipped} unchanged line${skipped === 1 ? '' : 's'}</div>`;
  return out;
}
