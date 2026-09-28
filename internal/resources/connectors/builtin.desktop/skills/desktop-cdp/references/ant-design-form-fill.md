# Ant Design 表单填写

一句话：Ant Design/React 表单不要只写 `el.value = text`，优先用原生 value setter 后派发冒泡的 `input` 和 `change` 事件，让 React 表单状态同步。

## When To Use

- The form filling workflow detected Ant Design or React-controlled inputs.
- 用户说输入框、下拉框或自动完成组件点击后消失、失焦、被清空。
- 普通 `Input.insertText`、鼠标点击、键盘输入不稳定。
- DOM 里的值变了，但 Ant Design Form 校验、提交值或页面状态没有同步。

## Stack Detection

Before using this reference, inspect the current page for Ant Design/React signals:

```js
(() => ({
  hasAntClasses: !!document.querySelector('[class*="ant-"], .ant-form, .ant-input, .ant-select'),
  hasReactRoot: !!document.querySelector('#root, [data-reactroot]'),
  hasReactFiber: !!document.querySelector('[class*="ant-"], input, textarea') &&
    Object.keys(document.querySelector('[class*="ant-"], input, textarea') || {})
      .some(key => key.startsWith('__reactFiber$') || key.startsWith('__reactProps$')),
  scripts: Array.from(document.scripts).map(s => s.src).filter(Boolean).slice(0, 20)
}))()
```

Use this reference when Ant Design classes or React-controlled field markers are present.

## Pattern

Use `Runtime.evaluate` after identifying the target field by stable selectors such as `id`, `name`, label text, or the nearest `.ant-form-item`.

```js
(() => {
  const el = document.querySelector('#start_addr');
  if (!el) return { ok: false, reason: 'field not found' };

  const setter = Object.getOwnPropertyDescriptor(
    window.HTMLInputElement.prototype,
    'value'
  ).set;

  setter.call(el, '陆家嘴');
  el.dispatchEvent(new Event('input', { bubbles: true }));
  el.dispatchEvent(new Event('change', { bubbles: true }));

  return { ok: true, value: el.value };
})()
```

For `textarea`, use `window.HTMLTextAreaElement.prototype`. For plain inputs, avoid assigning `el.value = text` unless it is only for visual inspection.

## Two Field Example

```js
(() => {
  const setReactInputValue = (selector, value) => {
    const el = document.querySelector(selector);
    if (!el) return { selector, ok: false, reason: 'not found' };

    const proto = el instanceof HTMLTextAreaElement
      ? window.HTMLTextAreaElement.prototype
      : window.HTMLInputElement.prototype;
    const setter = Object.getOwnPropertyDescriptor(proto, 'value')?.set;
    if (!setter) return { selector, ok: false, reason: 'setter not found' };

    setter.call(el, value);
    el.dispatchEvent(new Event('input', { bubbles: true }));
    el.dispatchEvent(new Event('change', { bubbles: true }));
    el.dispatchEvent(new Event('blur', { bubbles: true }));
    return { selector, ok: true, value: el.value };
  };

  return [
    setReactInputValue('#start_addr', '陆家嘴'),
    setReactInputValue('#end_addr', '静安寺')
  ];
})()
```

## Notes

- Always verify after filling by reading DOM values and, when useful, screenshot evidence.
- If the field is an Ant Design `Select`, `Cascader`, `DatePicker`, or `AutoComplete`, first inspect whether the real input is hidden and whether value state is controlled by a popup option; prefer selecting the real option if it is stable.
- Do not submit the form unless the user explicitly confirmed submission.
