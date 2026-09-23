export function radius(degree, zoom) { return Math.max(2, Math.sqrt(degree + 1) * 3) * zoom; }
export function visibleLabels(nodes, zoom) { return nodes.filter(n => n.degree >= 3 && zoom > 0.5); }
export const labelStyle = { size: 10.5, alpha: 0.55 };
// The renderer scales world radii with camera zoom. Pointer hit area is separately 14 screen px.
