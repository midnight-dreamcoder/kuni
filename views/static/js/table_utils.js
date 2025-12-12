/**
 * table_utils.js
 * Shared logic for client-side table filtering and sorting.
 */

// Universal Table Search / Filter
// Usage: <input onkeyup="filterTable('inputId', 'tableId')">
function filterTable(inputId, tableId) {
    let input = document.getElementById(inputId);
    if (!input) return;
    
    let filter = input.value.toLowerCase();
    let table = document.getElementById(tableId);
    if (!table) return;

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
// Usage: Add class "sortable-header" and data attributes to <th>
document.addEventListener('DOMContentLoaded', function() {
    const headers = document.querySelectorAll('th.sortable-header');
    headers.forEach(header => {
        header.addEventListener('click', () => {
            const table = header.closest('table');
            const tableBody = table.querySelector('tbody');
            const row = header.parentElement;
            
            // FIXED: Get the actual index of the TH among its siblings (tr.children)
            // This accounts for non-sortable columns like checkboxes
            const colIndex = Array.prototype.indexOf.call(row.children, header);

            const sortType = header.dataset.sortType || 'string'; // 'string', 'number', 'duration', 'size'
            const currentAsc = header.classList.contains('sort-asc');
            
            // Reset other headers in the same row
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
            // parseFloat handles "15 ms", "10 nodes" etc correctly
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
    // Prefer textContent to ignore HTML tags, trim whitespace
    return (cell.textContent || cell.innerText).trim();
}

// Helper: Parse "2d10h", "45m", "10s" into seconds
function parseDuration(str) {
    if (!str || str === "N/A" || str === "<none>") return -1;
    let total = 0;
    const days = str.match(/(\d+)d/);
    const hours = str.match(/(\d+)h/);
    const mins = str.match(/(\d+)m/);
    const secs = str.match(/(\d+)s/);
    
    if (days) total += parseInt(days[1]) * 86400;
    if (hours) total += parseInt(hours[1]) * 3600;
    if (mins) total += parseInt(mins[1]) * 60;
    if (secs) total += parseInt(secs[1]);
    
    // Fallback for just days like "25d" without other parts which above regex might miss if strict
    if (total === 0 && str.match(/^\d+d$/)) {
        return parseInt(str) * 86400;
    }
    
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