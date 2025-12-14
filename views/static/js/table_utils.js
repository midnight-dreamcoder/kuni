/**
 * table_utils.js
 * Shared logic for client-side table filtering and sorting.
 */

// Universal Table Search / Filter
function filterTable(inputId, tableId) {
    let input = document.getElementById(inputId);
    if (!input) return;
    
    let filter = input.value.toLowerCase();
    let table = document.getElementById(tableId);
    if (!table) return;

    // Support searching multiple tables if they share a class or ID pattern
    // But for now, stick to the main ID logic or standard tbody
    let tr = table.getElementsByTagName("tbody")[0].getElementsByTagName("tr");
    
    // Check if input is a valid regex
    let regex = null;
    try { 
        regex = new RegExp(filter, 'i'); 
        input.classList.remove('regex-error'); 
    } catch(e) { 
        input.classList.add('regex-error'); 
    }

    for (let i = 0; i < tr.length; i++) {
        if(tr[i].classList.contains('no-data')) continue;
        
        let rowText = tr[i].textContent || tr[i].innerText;
        if (regex && regex.test(rowText)) {
            tr[i].style.display = "";
        } else if (!regex && rowText.toLowerCase().indexOf(filter) > -1) {
            tr[i].style.display = "";
        } else {
            tr[i].style.display = "none";
        }
    }
}

// Universal Table Sort
document.addEventListener('DOMContentLoaded', function() {
    const headers = document.querySelectorAll('th.sortable-header');
    headers.forEach(header => {
        header.addEventListener('click', () => {
            const table = header.closest('table');
            const tableBody = table.querySelector('tbody');
            const row = header.parentElement;
            
            // Get actual column index (accounting for non-sortable cols like checkboxes)
            const colIndex = Array.prototype.indexOf.call(row.children, header);

            const sortType = header.dataset.sortType || 'string'; // 'string', 'number', 'duration', 'size'
            const currentAsc = header.classList.contains('sort-asc');
            
            // Reset other headers
            row.querySelectorAll('th').forEach(h => h.classList.remove('sort-asc', 'sort-desc'));
            
            // Toggle current
            if (!currentAsc) {
                header.classList.add('sort-asc');
            } else {
                header.classList.add('sort-desc');
            }
            
            sortRows(tableBody, colIndex, sortType, !currentAsc);
        });
    });
});

function sortRows(tableBody, colIndex, sortType, asc) {
    const rows = Array.from(tableBody.querySelectorAll('tr'));
    const dirModifier = asc ? 1 : -1;
    
    rows.sort((a, b) => {
        if(a.classList.contains('no-data')) return 1;
        if(b.classList.contains('no-data')) return -1;

        const aVal = getCellValue(a, colIndex);
        const bVal = getCellValue(b, colIndex);
        
        if (sortType === 'number') {
            return (parseFloat(aVal) - parseFloat(bVal)) * dirModifier;
        } else if (sortType === 'duration') {
            return (parseDuration(aVal) - parseDuration(bVal)) * dirModifier;
        } else if (sortType === 'size') {
            return (parseBytes(aVal) - parseBytes(bVal)) * dirModifier;
        }
        
        return aVal.localeCompare(bVal) * dirModifier;
    });

    rows.forEach(row => tableBody.appendChild(row));
}

function getCellValue(row, colIndex) {
    const cell = row.children[colIndex];
    if (!cell) return '';
    return (cell.textContent || cell.innerText).trim();
}

// Helper: Parse Kubernetes Age (e.g. "2y4d", "10h", "45m", "10s") into seconds
function parseDuration(str) {
    if (!str || str === "N/A" || str === "<none>") return -1;
    
    let total = 0;
    
    // Check for years
    const years = str.match(/(\d+)y/);
    if (years) total += parseInt(years[1]) * 31536000; // 365 * 24 * 3600

    // Check for days
    const days = str.match(/(\d+)d/);
    if (days) total += parseInt(days[1]) * 86400; // 24 * 3600

    // Check for hours
    const hours = str.match(/(\d+)h/);
    if (hours) total += parseInt(hours[1]) * 3600;

    // Check for minutes
    const mins = str.match(/(\d+)m/);
    if (mins) total += parseInt(mins[1]) * 60;

    // Check for seconds
    const secs = str.match(/(\d+)s/);
    if (secs) total += parseInt(secs[1]);
    
    return total;
}

// Helper: Parse "10Mi", "2Gi" into bytes
function parseBytes(str) {
    if (!str || str === "N/A") return -1;
    const units = { 'Ki': 1024, 'Mi': 1024**2, 'Gi': 1024**3, 'Ti': 1024**4, 'm': 0.001 };
    const match = str.match(/^([\d.]+)([A-Za-z]+)?$/);
    if (!match) return parseFloat(str) || 0;
    
    const val = parseFloat(match[1]);
    const unit = match[2];
    if (unit && units[unit]) return val * units[unit];
    return val;
}