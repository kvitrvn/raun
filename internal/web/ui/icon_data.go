// Lucide 0.576.0 icon paths (ISC), as bundled by shadcn-templ v2.0.0-beta.13.
// Only the icons used by the browser are kept. See ../licenses/lucide.txt.

package ui

// icons maps a Lucide icon name to the inner markup of its 24x24 SVG.
var icons = map[string]string{
	"book-open":                    `<path d="M12 7v14" /><path d="M3 18a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h5a4 4 0 0 1 4 4 4 4 0 0 1 4-4h5a1 1 0 0 1 1 1v13a1 1 0 0 1-1 1h-6a3 3 0 0 0-3 3 3 3 0 0 0-3-3z" />`,
	"bot":                          `<path d="M12 8V4H8" /><rect width="16" height="12" x="4" y="8" rx="2" /><path d="M2 14h2" /><path d="M20 14h2" /><path d="M15 13v2" /><path d="M9 13v2" />`,
	"check":                        `<path d="M20 6 9 17l-5-5" />`,
	"chevron-right":                `<path d="m9 18 6-6-6-6" />`,
	"circle-check":                 `<circle cx="12" cy="12" r="10" /><path d="m9 12 2 2 4-4" />`,
	"circle-question-mark":         `<circle cx="12" cy="12" r="10" /><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3" /><path d="M12 17h.01" />`,
	"circle-x":                     `<circle cx="12" cy="12" r="10" /><path d="m15 9-6 6" /><path d="m9 9 6 6" />`,
	"copy":                         `<rect width="14" height="14" x="8" y="8" rx="2" ry="2" /><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2" />`,
	"file-code":                    `<path d="M6 22a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.704.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2z" /><path d="M14 2v5a1 1 0 0 0 1 1h5" /><path d="M10 12.5 8 15l2 2.5" /><path d="m14 12.5 2 2.5-2 2.5" />`,
	"git-commit-horizontal":        `<circle cx="12" cy="12" r="3" /><line x1="3" x2="9" y1="12" y2="12" /><line x1="15" x2="21" y1="12" y2="12" />`,
	"inbox":                        `<polyline points="22 12 16 12 14 15 10 15 8 12 2 12" /><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z" />`,
	"layout-list":                  `<rect width="7" height="7" x="3" y="3" rx="1" /><rect width="7" height="7" x="3" y="14" rx="1" /><path d="M14 4h7" /><path d="M14 9h7" /><path d="M14 15h7" /><path d="M14 20h7" />`,
	"loader-circle":                `<path d="M21 12a9 9 0 1 1-6.219-8.56" />`,
	"lock":                         `<rect width="18" height="11" x="3" y="11" rx="2" ry="2" /><path d="M7 11V7a5 5 0 0 1 10 0v4" />`,
	"message-circle-question-mark": `<path d="M2.992 16.342a2 2 0 0 1 .094 1.167l-1.065 3.29a1 1 0 0 0 1.236 1.168l3.413-.998a2 2 0 0 1 1.099.092 10 10 0 1 0-4.777-4.719" /><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3" /><path d="M12 17h.01" />`,
	"panel-left":                   `<rect width="18" height="18" x="3" y="3" rx="2" /><path d="M9 3v18" />`,
	"refresh-cw":                   `<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8" /><path d="M21 3v5h-5" /><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16" /><path d="M8 16H3v5" />`,
	"scroll-text":                  `<path d="M15 12h-5" /><path d="M15 8h-5" /><path d="M19 17V5a2 2 0 0 0-2-2H4" /><path d="M8 21h12a2 2 0 0 0 2-2v-1a1 1 0 0 0-1-1H11a1 1 0 0 0-1 1v1a2 2 0 1 1-4 0V5a2 2 0 1 0-4 0v2a1 1 0 0 0 1 1h3" />`,
	"search":                       `<path d="m21 21-4.34-4.34" /><circle cx="11" cy="11" r="8" />`,
	"split":                        `<path d="M16 3h5v5" /><path d="M8 3H3v5" /><path d="M12 22v-8.3a4 4 0 0 0-1.172-2.872L3 3" /><path d="m15 9 6-6" />`,
	"target":                       `<circle cx="12" cy="12" r="10" /><circle cx="12" cy="12" r="6" /><circle cx="12" cy="12" r="2" />`,
	"triangle-alert":               `<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3" /><path d="M12 9v4" /><path d="M12 17h.01" />`,
	"user":                         `<path d="M19 21v-2a4 4 0 0 0-4-4H9a4 4 0 0 0-4 4v2" /><circle cx="12" cy="7" r="4" />`,
	"user-pen":                     `<path d="M11.5 15H7a4 4 0 0 0-4 4v2" /><path d="M21.378 16.626a1 1 0 0 0-3.004-3.004l-4.01 4.012a2 2 0 0 0-.506.854l-.837 2.87a.5.5 0 0 0 .62.62l2.87-.837a2 2 0 0 0 .854-.506z" /><circle cx="10" cy="7" r="4" />`,
	"user-round":                   `<circle cx="12" cy="8" r="5" /><path d="M20 21a8 8 0 0 0-16 0" />`,
	"users":                        `<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" /><path d="M16 3.128a4 4 0 0 1 0 7.744" /><path d="M22 21v-2a4 4 0 0 0-3-3.87" /><circle cx="9" cy="7" r="4" />`,
	"x":                            `<path d="M18 6 6 18" /><path d="m6 6 12 12" />`,
}
