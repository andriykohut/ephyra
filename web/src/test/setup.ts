import "@testing-library/jest-dom/vitest";
import { vi } from "vitest";

class RO {
  observe() {}
  unobserve() {}
  disconnect() {}
}
vi.stubGlobal("ResizeObserver", RO);

if (!window.matchMedia) {
  window.matchMedia = () =>
    ({
      matches: false,
      media: "",
      onchange: null,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
      dispatchEvent: () => false,
    }) as unknown as MediaQueryList;
}
