export function installViewportCssVars() {
  const update = () => {
    const vv = window.visualViewport;
    const layoutH = Math.round(window.innerHeight);
    const visualH = Math.round(vv ? vv.height : window.innerHeight);
    const offsetTop = Math.round(vv ? vv.offsetTop : 0);
    const keyboardBottom = Math.max(0, Math.round(layoutH - visualH - offsetTop));

    document.documentElement.style.setProperty("--appH", `${visualH}px`);
    document.documentElement.style.setProperty("--layoutH", `${layoutH}px`);
    document.documentElement.style.setProperty("--visualViewportH", `${visualH}px`);
    document.documentElement.style.setProperty("--visualViewportOffsetTop", `${offsetTop}px`);
    document.documentElement.style.setProperty("--keyboardBottom", `${keyboardBottom}px`);
  };
  update();
  window.addEventListener("resize", update);
  window.visualViewport?.addEventListener("resize", update);
  window.visualViewport?.addEventListener("scroll", update);
}
