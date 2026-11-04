# Ant Design 表单填写

Ant Design and other React-controlled widgets often ignore a value written straight into the DOM. Use AWCP first when the site publishes it; otherwise operate them with the typed surface tools, the same way on Chrome and WorkPanel. There is no script or raw CDP route.

一句话：输入框用 `fill`（或需要逐键触发联想时用 `type`），下拉、日期、级联等自定义组件先点击展开、读返回的观察、再点击选项，每一步都以返回的观察核对状态。

## When To Use

- The page shows Ant Design classes (`ant-select`, `ant-picker`, `ant-cascader`, `ant-form-item`) or other custom widgets in the observation.
- 输入框、下拉框或自动完成组件点击后消失、失焦或被清空。
- DOM 里的值变了，但表单校验、提交值或页面状态没有同步。

## Text inputs

1. `surface_observe`, then `surface_element` with the input's `ref`, `action: "fill"` and the full `value`. Fill replaces the value with real editing input, so React state updates.
2. For an AutoComplete or search box that reacts to keystrokes, use `action: "type"` (at most 2000 characters) instead, then read the returned observation for suggestions.
3. Confirm the value in the returned observation; a form-item validation message appears as nearby text or the element's `validationMessage`/`invalid` state.

## Select, Cascader and TreeSelect

1. Click the visible selector (`surface_click` with its ref). The dropdown renders in a popup at the end of the document; read it from the returned observation.
2. If the option list is long or virtualized, `type` part of the label into the focused search input, or `surface_scroll`/`press` `ArrowDown` and read the new viewport observation. Do not guess an option that is not observed.
3. Click the option ref from the newest observation. For multiple mode, repeat for each option, then `press` `Escape` to close the popup.
4. Verify the selected tag or text in the returned observation.

## DatePicker and TimePicker

1. Click the picker input, then `type` the date in the format the placeholder shows (for example `2026-10-23`) and `press` `Enter`.
2. If the picker rejects typed input, click the panel cells from the returned observation instead.
3. Verify the displayed value; a cleared field means the format was rejected.

## Checkbox, Radio and Switch

Use `surface_element` with `action: "check"` or `"uncheck"` on the observed control. It sends no input when the state already matches; a radio cannot be unchecked, so check the other option instead.

## Submit

Click the submit button only when the user's task authorizes submission, then read the returned observation for success messages, validation errors or a changed URL. Never resubmit after an unknown outcome; read state first.
