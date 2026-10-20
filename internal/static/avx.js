// avxEl(tag, props, ...children): the one DOM builder for data the page
// scripts render (H-1). Data goes in as text nodes and properties, never as
// markup, so an answer field cannot add elements or attributes. props:
// className, title, type, colSpan, style (cssText from constants), dataset
// {key: value} and on<event> handlers; anything else is refused (an href
// from data would need its own scheme check). Children are nodes or text.
function avxEl(tag, props) {
  if (['script', 'style', 'iframe', 'object', 'embed', 'link', 'meta', 'base'].indexOf(String(tag).toLowerCase()) >= 0) {
    throw new Error('avxEl: refused tag ' + tag);
  }
  const e = document.createElement(tag);
  const p = props || {};
  Object.keys(p).forEach(function (k) {
    const v = p[k];
    if (k === 'style') e.style.cssText = v;
    else if (k === 'dataset') Object.keys(v).forEach(function (d) { e.dataset[d] = String(v[d]); });
    else if (k.slice(0, 2) === 'on' && typeof v === 'function') e.addEventListener(k.slice(2), v);
    else if (k === 'className' || k === 'title' || k === 'type' || k === 'colSpan') e[k] = v;
    else throw new Error('avxEl: unsupported property ' + k);
  });
  for (let i = 2; i < arguments.length; i++) {
    const c = arguments[i];
    if (c == null || c === false) continue;
    e.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return e;
}
