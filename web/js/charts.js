/**
 * Charts and visualization module
 * Lightweight SVG-based charts for speed test results
 */

const Charts = (function() {
    'use strict';

    /**
     * Create an SVG element with namespace
     */
    function createSVG(tag, attrs = {}) {
        const el = document.createElementNS('http://www.w3.org/2000/svg', tag);
        for (const [key, value] of Object.entries(attrs)) {
            el.setAttribute(key, value);
        }
        return el;
    }

    /**
     * Create a sparkline chart
     * @param {HTMLElement} container - Container element
     * @param {number[]} data - Array of values
     * @param {Object} options - Chart options
     */
    function sparkline(container, data, options = {}) {
        const {
            width = 120,
            height = 40,
            strokeColor = 'var(--accent-primary)',
            fillColor = 'var(--accent-primary)',
            fillOpacity = 0.1,
            strokeWidth = 2,
            dotRadius = 0,
            animate = true
        } = options;

        if (!data || data.length === 0) {
            container.innerHTML = '';
            return;
        }

        const svg = createSVG('svg', {
            width,
            height,
            viewBox: `0 0 ${width} ${height}`,
            class: 'sparkline'
        });

        const min = Math.min(...data);
        const max = Math.max(...data);
        const range = max - min || 1;

        const padding = 4;
        const chartWidth = width - padding * 2;
        const chartHeight = height - padding * 2;

        // Calculate points
        const points = data.map((value, index) => {
            const x = padding + (index / (data.length - 1 || 1)) * chartWidth;
            const y = padding + chartHeight - ((value - min) / range) * chartHeight;
            return { x, y };
        });

        // Create fill area
        if (fillOpacity > 0) {
            const fillPoints = [...points];
            const areaPath = createSVG('path', {
                d: `M ${points[0].x} ${height - padding} ` +
                   points.map(p => `L ${p.x} ${p.y}`).join(' ') +
                   ` L ${points[points.length - 1].x} ${height - padding} Z`,
                fill: fillColor,
                'fill-opacity': fillOpacity,
                class: 'sparkline-area'
            });
            svg.appendChild(areaPath);
        }

        // Create line
        const linePath = createSVG('path', {
            d: `M ${points.map(p => `${p.x} ${p.y}`).join(' L ')}`,
            fill: 'none',
            stroke: strokeColor,
            'stroke-width': strokeWidth,
            'stroke-linecap': 'round',
            'stroke-linejoin': 'round',
            class: 'sparkline-line'
        });

        if (animate) {
            const pathLength = linePath.getTotalLength ? linePath.getTotalLength() : 500;
            linePath.style.strokeDasharray = pathLength;
            linePath.style.strokeDashoffset = pathLength;
            linePath.style.animation = 'sparkline-draw 0.8s ease-out forwards';
        }

        svg.appendChild(linePath);

        // Add end dot
        if (dotRadius > 0 && points.length > 0) {
            const lastPoint = points[points.length - 1];
            const dot = createSVG('circle', {
                cx: lastPoint.x,
                cy: lastPoint.y,
                r: dotRadius,
                fill: strokeColor,
                class: 'sparkline-dot'
            });
            svg.appendChild(dot);
        }

        container.innerHTML = '';
        container.appendChild(svg);

        return svg;
    }

    /**
     * Create a bar chart
     * @param {HTMLElement} container - Container element
     * @param {Array} data - Array of {label, value} objects
     * @param {Object} options - Chart options
     */
    function barChart(container, data, options = {}) {
        const {
            width = 300,
            height = 200,
            barColor = 'var(--accent-primary)',
            barRadius = 4,
            gap = 8,
            showLabels = true,
            showValues = true,
            animate = true
        } = options;

        if (!data || data.length === 0) {
            container.innerHTML = '';
            return;
        }

        const svg = createSVG('svg', {
            width,
            height,
            viewBox: `0 0 ${width} ${height}`,
            class: 'bar-chart'
        });

        const max = Math.max(...data.map(d => d.value));
        const padding = { top: 10, right: 10, bottom: showLabels ? 30 : 10, left: 10 };
        const chartWidth = width - padding.left - padding.right;
        const chartHeight = height - padding.top - padding.bottom;
        const barWidth = (chartWidth - gap * (data.length - 1)) / data.length;

        data.forEach((item, index) => {
            const barHeight = (item.value / max) * chartHeight;
            const x = padding.left + index * (barWidth + gap);
            const y = padding.top + chartHeight - barHeight;

            // Create bar
            const bar = createSVG('rect', {
                x,
                y: animate ? padding.top + chartHeight : y,
                width: barWidth,
                height: animate ? 0 : barHeight,
                rx: barRadius,
                fill: item.color || barColor,
                class: 'bar'
            });

            if (animate) {
                bar.style.transition = `all 0.4s ease-out ${index * 0.05}s`;
                requestAnimationFrame(() => {
                    bar.setAttribute('y', y);
                    bar.setAttribute('height', barHeight);
                });
            }

            svg.appendChild(bar);

            // Add value label
            if (showValues && item.value > 0) {
                const valueLabel = createSVG('text', {
                    x: x + barWidth / 2,
                    y: y - 5,
                    'text-anchor': 'middle',
                    'font-size': '10',
                    fill: 'var(--color-text-secondary)',
                    class: 'bar-value'
                });
                valueLabel.textContent = formatNumber(item.value);
                svg.appendChild(valueLabel);
            }

            // Add label
            if (showLabels) {
                const label = createSVG('text', {
                    x: x + barWidth / 2,
                    y: height - 8,
                    'text-anchor': 'middle',
                    'font-size': '10',
                    fill: 'var(--color-text-secondary)',
                    class: 'bar-label'
                });
                label.textContent = item.label;
                svg.appendChild(label);
            }
        });

        container.innerHTML = '';
        container.appendChild(svg);

        return svg;
    }

    /**
     * Create a horizontal progress bar
     * @param {HTMLElement} container - Container element
     * @param {number} value - Current value (0-100)
     * @param {Object} options - Options
     */
    function progressBar(container, value, options = {}) {
        const {
            height = 8,
            bgColor = 'var(--bg-tertiary)',
            fillColor = 'var(--accent-primary)',
            radius = 4,
            animate = true,
            showLabel = false
        } = options;

        const wrapper = document.createElement('div');
        wrapper.className = 'progress-bar-wrapper';
        wrapper.style.cssText = `
            position: relative;
            width: 100%;
            height: ${height}px;
            background: ${bgColor};
            border-radius: ${radius}px;
            overflow: hidden;
        `;

        const fill = document.createElement('div');
        fill.className = 'progress-bar-fill';
        fill.style.cssText = `
            position: absolute;
            left: 0;
            top: 0;
            height: 100%;
            background: ${fillColor};
            border-radius: ${radius}px;
            width: ${animate ? 0 : value}%;
            transition: width 0.6s ease-out;
        `;

        wrapper.appendChild(fill);

        if (showLabel) {
            const label = document.createElement('span');
            label.className = 'progress-bar-label';
            label.textContent = `${Math.round(value)}%`;
            label.style.cssText = `
                position: absolute;
                right: 8px;
                top: 50%;
                transform: translateY(-50%);
                font-size: 10px;
                color: var(--text-secondary);
            `;
            wrapper.appendChild(label);
        }

        container.innerHTML = '';
        container.appendChild(wrapper);

        if (animate) {
            requestAnimationFrame(() => {
                fill.style.width = `${value}%`;
            });
        }

        return wrapper;
    }

    /**
     * Create a latency scatter plot
     * @param {HTMLElement} container - Container element
     * @param {Array} samples - Array of latency samples
     * @param {Object} options - Options
     */
    function latencyPlot(container, samples, options = {}) {
        const {
            width = 300,
            height = 100,
            dotRadius = 4,
            dotColor = 'var(--accent-primary)',
            lineColor = 'var(--color-text-tertiary)',
            showStats = true
        } = options;

        if (!samples || samples.length === 0) {
            container.innerHTML = '<span class="no-data">No data</span>';
            return;
        }

        const svg = createSVG('svg', {
            width,
            height,
            viewBox: `0 0 ${width} ${height}`,
            class: 'latency-plot'
        });

        const values = samples.map(s => s.rttMs || s);
        const min = Math.min(...values);
        const max = Math.max(...values);
        const range = max - min || 1;

        const padding = { top: 15, right: 15, bottom: 15, left: 40 };
        const chartWidth = width - padding.left - padding.right;
        const chartHeight = height - padding.top - padding.bottom;

        // Add Y-axis labels
        const yLabels = [max, (max + min) / 2, min];
        yLabels.forEach((val, i) => {
            const y = padding.top + (i / 2) * chartHeight;
            const label = createSVG('text', {
                x: padding.left - 5,
                y: y + 4,
                'text-anchor': 'end',
                'font-size': '9',
                fill: 'var(--color-text-tertiary)'
            });
            label.textContent = `${Math.round(val)}`;
            svg.appendChild(label);

            // Add grid line
            const gridLine = createSVG('line', {
                x1: padding.left,
                y1: y,
                x2: width - padding.right,
                y2: y,
                stroke: lineColor,
                'stroke-opacity': 0.3,
                'stroke-dasharray': '2,2'
            });
            svg.appendChild(gridLine);
        });

        // Plot points
        samples.forEach((sample, index) => {
            const value = sample.rttMs || sample;
            const x = padding.left + (index / (samples.length - 1 || 1)) * chartWidth;
            const y = padding.top + chartHeight - ((value - min) / range) * chartHeight;

            const dot = createSVG('circle', {
                cx: x,
                cy: y,
                r: dotRadius,
                fill: dotColor,
                'fill-opacity': 0.7,
                class: 'latency-dot'
            });

            // Add tooltip on hover
            dot.innerHTML = `<title>${Math.round(value * 100) / 100} ms</title>`;
            svg.appendChild(dot);
        });

        container.innerHTML = '';
        container.appendChild(svg);

        // Add stats below chart
        if (showStats) {
            const statsDiv = document.createElement('div');
            statsDiv.className = 'latency-stats';
            statsDiv.innerHTML = `
                <span class="stat">Min: ${Math.round(min * 100) / 100}ms</span>
                <span class="stat">Median: ${Math.round(median(values) * 100) / 100}ms</span>
                <span class="stat">Max: ${Math.round(max * 100) / 100}ms</span>
            `;
            container.appendChild(statsDiv);
        }

        return svg;
    }

    /**
     * Create a packet loss visualization
     * @param {HTMLElement} container - Container element
     * @param {number} sent - Packets sent
     * @param {number} received - Packets received
     * @param {Object} options - Options
     */
    function packetLossBar(container, sent, received, options = {}) {
        const {
            height = 24,
            successColor = 'var(--success)',
            lossColor = 'var(--color-text-tertiary)',
            radius = 6
        } = options;

        const lossPercent = ((sent - received) / sent) * 100;
        const successPercent = 100 - lossPercent;

        const wrapper = document.createElement('div');
        wrapper.className = 'packet-loss-bar';
        wrapper.style.cssText = `
            display: flex;
            width: 100%;
            height: ${height}px;
            border-radius: ${radius}px;
            overflow: hidden;
            background: ${lossColor};
        `;

        const successBar = document.createElement('div');
        successBar.className = 'success-bar';
        successBar.style.cssText = `
            width: ${successPercent}%;
            height: 100%;
            background: ${successColor};
            transition: width 0.6s ease-out;
        `;

        wrapper.appendChild(successBar);

        container.innerHTML = '';
        container.appendChild(wrapper);

        // Add label
        const labelDiv = document.createElement('div');
        labelDiv.className = 'packet-loss-label';
        labelDiv.innerHTML = `
            <span class="received">${received}/${sent} packets received</span>
            <span class="loss">${lossPercent.toFixed(2)}% loss</span>
        `;
        container.appendChild(labelDiv);

        return wrapper;
    }

    /**
     * Create a gauge chart (half circle)
     * @param {HTMLElement} container - Container element
     * @param {number} value - Current value
     * @param {number} max - Maximum value
     * @param {Object} options - Options
     */
    function gauge(container, value, max, options = {}) {
        const {
            size = 120,
            strokeWidth = 10,
            bgColor = 'var(--bg-tertiary)',
            fillColor = 'var(--accent-primary)',
            showValue = true,
            unit = ''
        } = options;

        const svg = createSVG('svg', {
            width: size,
            height: size / 2 + 20,
            viewBox: `0 0 ${size} ${size / 2 + 20}`,
            class: 'gauge'
        });

        const centerX = size / 2;
        const centerY = size / 2;
        const radius = (size - strokeWidth) / 2;

        // Background arc
        const bgArc = createSVG('path', {
            d: describeArc(centerX, centerY, radius, 180, 360),
            fill: 'none',
            stroke: bgColor,
            'stroke-width': strokeWidth,
            'stroke-linecap': 'round'
        });
        svg.appendChild(bgArc);

        // Value arc
        const percentage = Math.min(value / max, 1);
        const angle = 180 + percentage * 180;
        const valueArc = createSVG('path', {
            d: describeArc(centerX, centerY, radius, 180, angle),
            fill: 'none',
            stroke: fillColor,
            'stroke-width': strokeWidth,
            'stroke-linecap': 'round',
            class: 'gauge-value'
        });
        svg.appendChild(valueArc);

        // Value text
        if (showValue) {
            const text = createSVG('text', {
                x: centerX,
                y: centerY + 5,
                'text-anchor': 'middle',
                'font-size': '18',
                'font-weight': '600',
                fill: 'var(--color-text-primary)'
            });
            text.textContent = formatNumber(value) + unit;
            svg.appendChild(text);
        }

        container.innerHTML = '';
        container.appendChild(svg);

        return svg;
    }

    /**
     * Create a box plot (candle bar) chart showing distribution
     * @param {HTMLElement} container - Container element
     * @param {number[]} data - Array of values
     * @param {Object} options - Chart options
     */
    function boxPlot(container, data, options = {}) {
        const {
            width = 300,
            height = 60,
            barColor = 'var(--accent-primary)',
            whiskerColor = 'var(--color-text-tertiary)',
            medianColor = 'var(--color-text-primary)',
            avgColor = 'var(--color-text-secondary)',
            barHeight = 24,
            showLabels = true,
            unit = 'Mbps'
        } = options;

        if (!data || data.length < 2) {
            container.innerHTML = '<span class="no-data">Not enough data</span>';
            return;
        }

        const sorted = [...data].sort((a, b) => a - b);
        const min = sorted[0];
        const max = sorted[sorted.length - 1];
        const med = percentile(sorted, 50);
        const p25 = percentile(sorted, 25);
        const p75 = percentile(sorted, 75);
        const avg = data.reduce((a, b) => a + b, 0) / data.length;

        // If all values are the same, just show a point
        if (min === max) {
            container.innerHTML = `<span class="no-data">${formatNumber(min)} ${unit}</span>`;
            return;
        }

        // Use fixed viewBox but responsive width/height via CSS
        const svg = createSVG('svg', {
            viewBox: `0 0 ${width} ${height}`,
            preserveAspectRatio: 'xMidYMid meet',
            class: 'box-plot'
        });

        const padding = showLabels ? 40 : 10;
        const chartWidth = width - padding * 2;
        const centerY = height / 2;

        // Scale function
        const scale = (val) => padding + ((val - min) / (max - min)) * chartWidth;

        // Whisker line (min to max)
        const whiskerLine = createSVG('line', {
            x1: scale(min),
            y1: centerY,
            x2: scale(max),
            y2: centerY,
            stroke: whiskerColor,
            'stroke-width': 2
        });
        svg.appendChild(whiskerLine);

        // Min whisker cap
        const minCap = createSVG('line', {
            x1: scale(min),
            y1: centerY - 8,
            x2: scale(min),
            y2: centerY + 8,
            stroke: whiskerColor,
            'stroke-width': 2
        });
        svg.appendChild(minCap);

        // Max whisker cap
        const maxCap = createSVG('line', {
            x1: scale(max),
            y1: centerY - 8,
            x2: scale(max),
            y2: centerY + 8,
            stroke: whiskerColor,
            'stroke-width': 2
        });
        svg.appendChild(maxCap);

        // IQR box (25th to 75th percentile)
        const box = createSVG('rect', {
            x: scale(p25),
            y: centerY - barHeight / 2,
            width: scale(p75) - scale(p25),
            height: barHeight,
            fill: barColor,
            opacity: 0.6,
            rx: 4
        });
        svg.appendChild(box);

        // Median line (solid)
        const medianLine = createSVG('line', {
            x1: scale(med),
            y1: centerY - barHeight / 2 - 2,
            x2: scale(med),
            y2: centerY + barHeight / 2 + 2,
            stroke: medianColor,
            'stroke-width': 3,
            'stroke-linecap': 'round'
        });
        svg.appendChild(medianLine);

        // Average line (dashed)
        const avgLine = createSVG('line', {
            x1: scale(avg),
            y1: centerY - barHeight / 2 - 2,
            x2: scale(avg),
            y2: centerY + barHeight / 2 + 2,
            stroke: avgColor,
            'stroke-width': 2,
            'stroke-dasharray': '4,3',
            'stroke-linecap': 'round'
        });
        svg.appendChild(avgLine);

        // Labels
        if (showLabels) {
            // Min label
            const minLabel = createSVG('text', {
                x: scale(min),
                y: height - 4,
                'text-anchor': 'middle',
                'font-size': '11',
                'font-weight': '500',
                fill: 'var(--color-text-secondary)'
            });
            minLabel.textContent = formatNumber(min);
            svg.appendChild(minLabel);

            // Max label
            const maxLabel = createSVG('text', {
                x: scale(max),
                y: height - 4,
                'text-anchor': 'middle',
                'font-size': '11',
                'font-weight': '500',
                fill: 'var(--color-text-secondary)'
            });
            maxLabel.textContent = formatNumber(max);
            svg.appendChild(maxLabel);

            // Median label (top)
            const medLabel = createSVG('text', {
                x: scale(med),
                y: 10,
                'text-anchor': 'middle',
                'font-size': '12',
                'font-weight': '600',
                fill: 'var(--color-text-primary)'
            });
            medLabel.textContent = formatNumber(med);
            svg.appendChild(medLabel);
        }

        container.innerHTML = '';
        container.appendChild(svg);

        // Return stats for tooltip/details
        return {
            min,
            max,
            median: med,
            average: avg,
            p25,
            p75,
            svg
        };
    }

    /**
     * Helper: Calculate percentile
     */
    function percentile(sortedArr, p) {
        if (sortedArr.length === 0) return 0;
        if (sortedArr.length === 1) return sortedArr[0];

        const index = (p / 100) * (sortedArr.length - 1);
        const lower = Math.floor(index);
        const upper = Math.ceil(index);
        const weight = index - lower;

        if (upper >= sortedArr.length) return sortedArr[sortedArr.length - 1];
        if (lower === upper) return sortedArr[lower];

        return sortedArr[lower] * (1 - weight) + sortedArr[upper] * weight;
    }

    /**
     * Helper: Describe SVG arc path
     */
    function describeArc(x, y, radius, startAngle, endAngle) {
        const start = polarToCartesian(x, y, radius, endAngle);
        const end = polarToCartesian(x, y, radius, startAngle);
        const largeArcFlag = endAngle - startAngle <= 180 ? 0 : 1;

        return [
            'M', start.x, start.y,
            'A', radius, radius, 0, largeArcFlag, 0, end.x, end.y
        ].join(' ');
    }

    /**
     * Helper: Convert polar to cartesian coordinates
     */
    function polarToCartesian(centerX, centerY, radius, angleInDegrees) {
        const angleInRadians = (angleInDegrees - 90) * Math.PI / 180.0;
        return {
            x: centerX + (radius * Math.cos(angleInRadians)),
            y: centerY + (radius * Math.sin(angleInRadians))
        };
    }

    /**
     * Helper: Calculate median
     */
    function median(arr) {
        if (arr.length === 0) return 0;
        const sorted = [...arr].sort((a, b) => a - b);
        const mid = Math.floor(sorted.length / 2);
        return sorted.length % 2 !== 0 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2;
    }

    /**
     * Helper: Format number
     */
    function formatNumber(num) {
        if (num >= 1000) {
            return (num / 1000).toFixed(1) + 'k';
        }
        if (num >= 100) {
            return Math.round(num);
        }
        return Math.round(num * 10) / 10;
    }

    /**
     * Update chart with animation
     */
    function updateSparkline(container, newData, options = {}) {
        sparkline(container, newData, { ...options, animate: true });
    }

    // Add CSS for animations
    const style = document.createElement('style');
    style.textContent = `
        @keyframes sparkline-draw {
            to {
                stroke-dashoffset: 0;
            }
        }

        .sparkline, .bar-chart, .latency-plot, .gauge, .box-plot {
            display: block;
        }

        .box-plot {
            overflow: visible;
        }

        .latency-stats {
            display: flex;
            justify-content: space-between;
            margin-top: 8px;
            font-size: 11px;
            color: var(--color-text-secondary);
        }

        .packet-loss-label {
            display: flex;
            justify-content: space-between;
            margin-top: 8px;
            font-size: 12px;
        }

        .packet-loss-label .received {
            color: var(--color-text-secondary);
        }

        .packet-loss-label .loss {
            color: var(--color-warning);
        }

        .no-data {
            display: block;
            text-align: center;
            color: var(--color-text-tertiary);
            font-size: 12px;
            padding: 20px;
        }
    `;
    document.head.appendChild(style);

    // Public API
    return {
        sparkline,
        barChart,
        boxPlot,
        progressBar,
        latencyPlot,
        packetLossBar,
        gauge,
        updateSparkline,
        formatNumber,
        median,
        percentile
    };
})();

// Export for module systems
if (typeof module !== 'undefined' && module.exports) {
    module.exports = Charts;
}
