# T7 frontend — verified `fuxsto-design` evidence

Empirically determined against the **installed** `fuxsto-design@1.0.5`
(`node_modules/fuxsto-design`), not from the registry listing. Recorded here
because plan §4.4 / appendix A.6 assert this library's shape from metadata only.

## Package identity (confirmed)

| Field | Value |
|---|---|
| Version resolved by `~1.0.5` | **1.0.5** |
| License | MIT |
| Published | 2026-10-02 |
| `type` | `module` (ESM only — no CJS entry) |
| Peer deps | `vue ^3.5.0`, `tailwindcss ^4.0.0`, `lucide-vue-next ^0.577.0` |
| Deps | `clsx ^2.1.1`, `tailwind-merge ^3.6.0`, `@floating-ui/vue ^2.0.1` |
| Subpath exports | **82** (matches the plan's claim exactly) |

## CSS entry — WORKS as the plan predicted ✅

```css
@import 'tailwindcss';
@import 'fuxsto-design/styles';
```

`./styles` resolves to `node_modules/fuxsto-design/dist/styles.css`
(112 447 bytes, pre-compiled, self-contained). `./styles.css` is an alias for the
same file. **No `@source` / safelist configuration is needed** — confirmed: the
dev server serves `src/style.css` as 165 KB of compiled CSS including the
library's `--color-background` theme tokens and utility classes.

## Import styles — both work ✅

```ts
import { Button, Card, Table } from 'fuxsto-design'   // root barrel
import Button from 'fuxsto-design/button'             // per-component subpath
```

**There is no `install()` plugin function.** Components are imported directly
(no `app.use(FuxstoDesign)`).

## Real export list (82 subpaths, verbatim from `package.json#exports`)

```
. cn tag card chip form link list menu rate tabs text tour tree alert badge
empty image input radio steps table title anchor avatar button dialog drawer
header result select slider styles switch upload divider loading message
tooltip back-top carousel cascader checkbox collapse progress skeleton textarea
timeline transfer countdown form-item list-item paragraph pin-input segmented
statistic tab-views tag-group watermark breadcrumb chip-group pagination
popconfirm styles.css date-picker radio-group scroll-area time-picker
avatar-group button-group color-picker context-menu input-number notification
virtual-list auto-complete collapse-item timeline-item checkbox-group
streaming-text breadcrumb-item contribution-chart
```

All 43 subpaths used by the `components/ui` adapter were compile-verified to
resolve under `vue-tsc`.

## Props verified from the installed `.d.ts` files

| Component | Key props actually present |
|---|---|
| `button` | `variant: primary\|secondary\|outline\|ghost\|glass`, `size: sm\|md\|lg`, `disabled`, `loading`, `danger`, `icon`, `iconPosition`, `suffixIcon`, `round` |
| `card` | `variant`, `shadow`, `bordered`, `padding: none\|sm\|md\|lg`, `hoverShadow`, `interactive: none\|press\|lift`, `loading`; slots `header` / `default` / `footer` |
| `badge` | `value`, `max`, `dot`, `pulse`, `variant: default\|primary\|secondary\|outline\|destructive`, `size`, `round`, `icon` |
| `tabs` | `modelValue`, `options: {label,value,disabled,icon}[]`, `variant: pill\|line`, `size`, `bordered` |
| `table` | `columns: TableColumn[]`, `data`, `rowKey`, `size`, `bordered`, `striped`, `hover`, `selectable`, `emptyText`, `loading`; scoped slot `` #cell-${key}="{ row, column, value, index }" `` |
| `form` / `form-item` | Form: `model`, `rules`, `disabled`, `size`, exposes `validate()`/`resetFields()`; FormItem: `prop`, `label`, `required`, `error` |
| `input` | `modelValue: string\|number`, `type`, `placeholder`, `size`, `disabled`, `readonly`, `clearable`, `error`, `prefixIcon`, `suffixIcon` |
| `input-number` | `modelValue: number\|null`, `min`, `max`, `step`, `precision`, `controls` |
| `select` | `modelValue: string\|number`, **`options` prop (no `<option>` slots)**, `searchable`, `clearable`, `error` |
| `switch` | `modelValue: boolean`, `size`, `disabled` |
| `textarea` | `modelValue`, `rows`, `autoSize`, `showCount`, `resize`, `maxlength` |
| `slider` | `modelValue: number\|[number,number]`, `min`, `max`, `step`, `range`, `status` |
| `alert` | `type`, `title`, `description`, `banner`, `showIcon`, `closable`; slots `title` / `default` / `extra` |
| `empty` | `title`, `description`, `image`, `icon`, `size`, `variant: default\|card\|dashed`; slot `extra` |
| `virtual-list` | `items`, `itemHeight`, `height`, `overscan`, `keyField`, `showEmpty`; slot `item` |
| `tree` | `treeData: TreeNode[]`, `checkable`, `selectable`, `expandedKeys`, `defaultExpandAll`, `expandOnClickNode` |
| `popconfirm` | `title`, `description`, `danger`, `confirmText`, `cancelText`, `beforeConfirm()`, events `confirm`/`cancel` |
| `upload` | `modelValue: UploadFile[]`, `accept`, `multiple`, `drag`, `action`, `maxCount`, `autoUpload`, `beforeUpload` |
| `message` | imperative: `Message.success/info/warning/error(content, opts)` → returns id |
| `dialog` | imperative: `Dialog({title,description,content,...})` → `{close, setLoading}` |

### Two plan corrections worth recording

1. **The plan's `card` `statistic` mapping is fine, but `statistic` is
   animation-oriented** (`from`, `duration`) and oversized for a dense instance
   grid. The shim ships `UiStatTile` for that density, and still re-exports the
   library's `UiStatistic`.
2. **`select` takes an `options` prop, not children.** Copy-pasting a generic
   shadcn `<Select><Option/></Select>` snippet will not compile.

## What the library genuinely does NOT provide

Confirmed absent from all 82 exports — the plan's "关键缺口" list is accurate:

- **No terminal component** → the console uses `@xterm/xterm` (never `<pre>` /
  `<textarea>`; that would drop ANSI colour and fail D1).
- **No chart component.** `contribution-chart` is a GitHub-style heatmap and is
  not usable for time series → metrics use `uplot`.

## Shim inventory (`components/ui/*`)

Everything is routed through the adapter; **no page imports
`fuxsto-design/*` directly.** Local additions:

| Shim | Why |
|---|---|
| `cn.ts` | class merge (re-exports the same `clsx` + `tailwind-merge` behaviour) |
| `types.ts` | local mirrors of `TableColumn` / `UploadFile` / `TabOption` / `SelectOption` / `TreeNode` so third-party types never leak into app code |
| `UiKeyValue.vue` | label/value row for detail panels (no library equivalent) |
| `UiSectionNote.vue` | inline explanatory note (no library equivalent) |
| `UiStatTile.vue` | compact stat tile for dense grids (library `statistic` is animation-sized) |
| `UiNotAvailable.vue` | standardised 501 "not available in this build" state |

Hosted elsewhere because they wrap a non-library engine:
`components/console/XtermTerminal.vue` (xterm.js) and
`components/monitor/MetricChart.vue` (uplot).
