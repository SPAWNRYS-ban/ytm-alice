export function icon(name) {
 const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');
 svg.classList.add('icon');svg.setAttribute('aria-hidden','true');svg.setAttribute('focusable','false');
 const use=document.createElementNS('http://www.w3.org/2000/svg','use');
 use.setAttribute('href','/icons.svg#'+name);svg.append(use);return svg;
}
