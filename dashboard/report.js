'use strict';

// ── Chart.js defaults ──
Chart.defaults.color = '#64748b';
Chart.defaults.borderColor = '#232d47';
Chart.defaults.font.family = "'JetBrains Mono', monospace";

const chartOpts = (yLabel, yUnit = '') => ({
  responsive: true,
  maintainAspectRatio: true,
  animation: false, // Turn off animation since it is static rendering
  plugins: {
    legend: { display: true, labels: { boxWidth: 10, padding: 12 } },
    tooltip: {
      callbacks: {
        label: (ctx) => ` ${ctx.dataset.label}: ${ctx.parsed.y.toLocaleString()}${yUnit}`
      }
    }
  },
  scales: {
    x: { grid: { color: '#1a2340' }, ticks: { maxTicksLimit: 10, maxRotation: 0 } },
    y: {
      grid: { color: '#1a2340' }, beginAtZero: true,
      title: { display: !!yLabel, text: yLabel, color: '#64748b' }
    }
  }
});

document.addEventListener('DOMContentLoaded', async () => {
    const params = new URLSearchParams(window.location.search);
    const testId = params.get('testId');
    const token = params.get('token');

    if (!testId || token === null) {
        document.getElementById('loading').innerHTML = '<span style="color:var(--red);">Error: Missing testId or token in URL parameters.</span>';
        return;
    }

    try {
        const response = await fetch(`/logs/report_${testId}.json?token=${encodeURIComponent(token)}`);
        if (!response.ok) {
            throw new Error(`Report not found or access denied (HTTP ${response.status})`);
        }
        
        const report = await response.json();
        renderReport(report);
        
        document.getElementById('loading').style.display = 'none';
        document.getElementById('reportContent').style.display = 'block';
    } catch (err) {
        document.getElementById('loading').innerHTML = `<span style="color:var(--red);">Error loading report: ${err.message}</span>`;
    }
});

function setText(id, text) {
    const el = document.getElementById(id);
    if (el) el.textContent = text;
}

function renderReport(r) {
    // Meta
    setText('r-testid', r.meta.test_id);
    setText('r-target', r.config.target);
    setText('r-date', new Date(r.meta.generated_at).toLocaleString());
    setText('r-operator', r.meta.operator);

    // Config
    setText('c-type', r.config.test_type);
    setText('c-method', r.config.method);
    setText('c-rps', `${r.config.peak_rps} ${r.config.unit}`);
    
    // Duration calc
    const durSec = r.config.total_duration / 1000000000;
    setText('c-dur', durSec > 60 ? `${(durSec/60).toFixed(1)} min` : `${durSec.toFixed(0)} sec`);
    
    setText('c-h2', r.config.http2 ? 'Yes' : 'No');
    setText('c-ka', r.config.keep_alive ? 'Yes' : 'No');
    setText('c-workers', r.config.max_workers);
    const uaDisplay = r.config.user_agent_prefix ? `Custom: ${r.config.user_agent_prefix}` : 'Default (Random Pool)';
    setText('c-ua', uaDisplay);

    if (r.config.headers && Object.keys(r.config.headers).length > 0) {
        document.getElementById('c-headers-row').style.display = 'table-row';
        const container = document.getElementById('c-headers');
        container.innerHTML = Object.entries(r.config.headers).map(([k, v]) => 
            `<div style="display: flex; margin-bottom: 6px; border-bottom: 1px solid rgba(255,255,255,0.05); padding-bottom: 4px;">
                <div style="flex: 0 0 160px; color: var(--accent2); font-weight: 500; font-size: 0.75rem;">${k}</div>
                <div style="flex: 1; color: var(--text); word-break: break-all; font-size: 0.8rem;">${v}</div>
            </div>`
        ).join('');
    }

    // Summary
    setText('s-req', r.summary.total_requests.toLocaleString());
    const succPct = (r.summary.overall_success_rate * 100).toFixed(1);
    const errPct = (r.summary.overall_error_rate * 100).toFixed(2);
    setText('s-succ', `Success: ${r.summary.success_count.toLocaleString()} (${succPct}%)`);
    
    setText('s-peak-rps', `${r.summary.peak_rps.toFixed(0)} RPS`);
    setText('s-peak-tps', `Max TPS: ${r.summary.peak_tps.toFixed(0)}`);
    
    setText('s-err', `${errPct}%`);
    setText('s-tmo', `Timeouts: ${r.summary.timeout_count.toLocaleString()}`);
    
    // Error Breakdown textual display
    const errBD = document.getElementById('s-err-breakdown');
    if (r.summary.status_codes) {
        const codes = Object.entries(r.summary.status_codes)
            .filter(([k]) => !k.startsWith('2'))
            .sort((a,b) => b[1] - a[1]);
        if (codes.length > 0) {
            errBD.innerHTML = codes.slice(0, 3).map(([k,v]) => `• ${k}: ${v.toLocaleString()}`).join('<br>');
        }
    }

    if (r.summary.overall_error_rate > 0.05) {
        document.getElementById('s-err').style.color = 'var(--red)';
        document.getElementById('s-card-err').style.borderColor = 'rgba(239,68,68,0.5)';
    }

    setText('s-lat', `${r.summary.avg_latency_ms.toFixed(1)} ms`);
    setText('s-p99', `p99: ${r.summary.p99_latency_ms.toFixed(1)} ms`);
    if (r.summary.avg_latency_ms > 1000) {
        document.getElementById('s-lat').style.color = 'var(--yellow)';
    }

    // Analysis
    const a = r.analysis;
    setText('a-brk', a.breaking_point_rps > 0 ? `${a.breaking_point_rps.toFixed(0)} RPS` : '—');
    setText('a-ld', a.latency_degradation_rps > 0 ? `${a.latency_degradation_rps.toFixed(0)} RPS` : '—');
    setText('a-rl', a.rate_limit_rps > 0 ? `${a.rate_limit_rps.toFixed(0)} RPS` : '—');
    setText('a-rec', a.recovery_observed ? '✅ Yes' : '❌ No');

    const obsEl = document.getElementById('a-obs');
    obsEl.innerHTML = a.observations.map(o => {
        const isCrit = o.toLowerCase().includes('exceeded') || o.toLowerCase().includes('high') || o.toLowerCase().includes('rate');
        const cls = isCrit ? 'obs-warn' : '';
        return `<div class="obs-item ${cls}">${o}</div>`;
    }).join('');

    // Charts
    const timeLabels = [];
    const rpsData = [];
    const tpsData = [];
    const latAvg = [];
    const latP95 = [];
    const latP99 = [];
    const errData = [];

    r.timeline.forEach(snap => {
        timeLabels.push(new Date(snap.timestamp).toLocaleTimeString('en-GB', { hour12: false }));
        rpsData.push(snap.rps || 0);
        tpsData.push(snap.tps || 0);
        latAvg.push(+(snap.avg_latency_ms || 0).toFixed(1));
        latP95.push(+(snap.p95_latency_ms || 0).toFixed(1));
        latP99.push(+(snap.p99_latency_ms || 0).toFixed(1));
        errData.push(+((snap.error_rate || 0) * 100).toFixed(2));
    });

    // RPS Chart
    new Chart(document.getElementById('rpsChart'), {
      type: 'line',
      data: {
        labels: timeLabels,
        datasets: [{
          label: 'RPS (Load)',
          data: rpsData,
          borderColor: '#6366f1',
          backgroundColor: 'transparent',
          borderWidth: 2,
          fill: false,
          tension: 0.2,
          pointRadius: 0,
        },
        {
          label: 'TPS (Success)',
          data: tpsData,
          borderColor: '#10b981',
          backgroundColor: 'rgba(16,185,129,0.1)',
          borderWidth: 2,
          fill: true,
          tension: 0.2,
          pointRadius: 0,
        }]
      },
      options: chartOpts('Requests/sec', ' req/s')
    });

    // Latency Chart
    new Chart(document.getElementById('latChart'), {
      type: 'line',
      data: {
        labels: timeLabels,
        datasets: [
          { label: 'Avg', data: latAvg, borderColor: '#06b6d4', backgroundColor: 'transparent', borderWidth: 2, fill: false, tension: 0.2, pointRadius: 0 },
          { label: 'p95', data: latP95, borderColor: '#f59e0b', backgroundColor: 'transparent', borderWidth: 1.5, fill: false, tension: 0.2, pointRadius: 0, borderDash: [4, 3] },
          { label: 'p99', data: latP99, borderColor: '#ef4444', backgroundColor: 'transparent', borderWidth: 1.5, fill: false, tension: 0.2, pointRadius: 0, borderDash: [2, 3] },
        ]
      },
      options: chartOpts('Latency', ' ms')
    });

    // Error Chart
    new Chart(document.getElementById('errChart'), {
      type: 'line',
      data: {
        labels: timeLabels,
        datasets: [{
          label: 'Error Rate',
          data: errData,
          borderColor: '#ef4444',
          backgroundColor: 'rgba(239,68,68,0.15)',
          borderWidth: 2,
          fill: true,
          tension: 0.2,
          pointRadius: 0,
        }]
      },
      options: {
        ...chartOpts('Error Rate', '%'),
        scales: {
          x: { grid: { color: '#1a2340' }, ticks: { maxTicksLimit: 10, maxRotation: 0 } },
          y: {
            grid: { color: '#1a2340' }, beginAtZero: true, max: 100,
            ticks: { callback: v => v + '%' }
          }
        }
      }
    });

    // Status Code Chart
    const scCumulative = {};
    r.timeline.forEach(snap => {
        if (snap.status_codes) {
            for (const [code, count] of Object.entries(snap.status_codes)) {
                scCumulative[code] = (scCumulative[code] || 0) + count;
            }
        }
    });

    const codes = Object.keys(scCumulative).sort();
    const scData = codes.map(c => scCumulative[c]);
    const bgColors = codes.map(c => {
        if (c === 'TIMEOUT') return '#8b5cf644';  // Purple for timeout
        if (c.startsWith('2')) return '#22c55e44';
        if (c.startsWith('3')) return '#06b6d444';
        if (c.startsWith('4')) return '#f59e0b44';
        if (c.startsWith('5')) return '#ef444444';
        return '#94a3b844';
    });
    const borderColors = codes.map(c => {
        if (c === 'TIMEOUT') return '#8b5cf6';
        if (c.startsWith('2')) return '#22c55e';
        if (c.startsWith('3')) return '#06b6d4';
        if (c.startsWith('4')) return '#f59e0b';
        if (c.startsWith('5')) return '#ef4444';
        return '#94a3b8';
    });

    new Chart(document.getElementById('scChart'), {
      type: 'bar',
      data: {
        labels: codes,
        datasets: [{
          label: 'Responses',
          data: scData,
          backgroundColor: bgColors,
          borderColor: borderColors,
          borderWidth: 2,
          borderRadius: 4,
        }]
      },
      options: {
        ...chartOpts('Count'),
        plugins: { legend: { display: false } }
      }
    });
}
