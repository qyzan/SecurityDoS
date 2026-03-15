let allReports = [];

async function fetchReports() {
    const token = localStorage.getItem('dos_token') || '';
    try {
        const response = await fetch(`/api/reports?token=${token}`);
        const data = await response.json();
        
        if (response.ok) {
            allReports = data;
            renderReports(allReports);
        } else {
            console.error('Failed to fetch reports:', data.error);
            document.getElementById('reportsBody').innerHTML = `<tr><td colspan="6" style="text-align:center; padding:40px; color:#ef4444;">Error: ${data.error}</td></tr>`;
        }
    } catch (err) {
        console.error('Fetch error:', err);
        document.getElementById('reportsBody').innerHTML = `<tr><td colspan="6" style="text-align:center; padding:40px; color:#ef4444;">Connection failed</td></tr>`;
    }
}

function renderReports(reports) {
    const body = document.getElementById('reportsBody');
    if (!reports || reports.length === 0) {
        body.innerHTML = '<tr><td colspan="6" style="text-align:center; padding:40px; color:var(--muted);">No reports found</td></tr>';
        return;
    }

    body.innerHTML = reports.map(r => {
        const date = new Date(r.generated_at).toLocaleString();
        const successRate = (r.success_rate * 100).toFixed(1);
        let badgeClass = 'badge-success';
        if (successRate < 90) badgeClass = 'badge-warn';
        if (successRate < 50) badgeClass = 'badge-error';

        const sizeKB = (r.size / 1024).toFixed(1) + ' KB';

        return `
            <tr>
                <td style="font-family: monospace; font-size: 0.85rem;">${date}</td>
                <td style="max-width: 300px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;" title="${r.target}">${r.target}</td>
                <td><span class="badge" style="background:rgba(99,102,241,0.1); color:#818cf8;">${r.test_type}</span></td>
                <td><span class="badge ${badgeClass}">${successRate}%</span></td>
                <td style="color:var(--muted); font-size:0.8rem;">${sizeKB}</td>
                <td>
                    <button class="btn btn-secondary" style="padding: 4px 12px; font-size: 0.75rem;" onclick="viewReport('${r.test_id}')">👁️ View</button>
                </td>
            </tr>
        `;
    }).join('');
}

function viewReport(testId) {
    const token = localStorage.getItem('dos_token') || '';
    window.open(`/report.html?testId=${testId}&token=${token}`, '_blank');
}

function filterReports() {
    const q = document.getElementById('searchInput').value.toLowerCase();
    const filtered = allReports.filter(r => 
        r.target.toLowerCase().includes(q) || 
        r.test_id.toLowerCase().includes(q)
    );
    renderReports(filtered);
}

// Initial fetch
document.addEventListener('DOMContentLoaded', fetchReports);
