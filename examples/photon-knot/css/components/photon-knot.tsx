"use client";

import {
  forwardRef,
  useEffect,
  useImperativeHandle,
  useRef,
  useSyncExternalStore,
  type CSSProperties,
  type ReactNode,
} from "react";
import * as THREE from "three";
import "./photon-knot.css";

export type PhotonKnotShape = "trefoil" | "cinquefoil" | "septafoil" | "star" | "tangle" | "halo";

export interface PhotonKnotHandle {
  flip: (position?: number, strength?: number) => void;
  spin: (velocity?: number) => void;
  replay: () => void;
}

export interface PhotonKnotProps {
  colors?: [string, string];
  backgroundColor?: string;
  shape?: PhotonKnotShape;
  size?: number;
  offsetX?: number;
  offsetY?: number;
  ribbonWidth?: number;
  loop?: number;
  twists?: number;
  flowSpeed?: number;
  rotationSpeed?: number;
  tilt?: number;
  glow?: number;
  edges?: number;
  folds?: number;
  sheen?: number;
  threads?: number;
  depth?: number;
  parallax?: number;
  interactive?: boolean;
  draggable?: boolean;
  flip?: number;
  bloom?: number;
  grain?: number;
  intro?: boolean;
  paused?: boolean;
  dpr?: number;
  children?: ReactNode;
  className?: string;
  style?: CSSProperties;
}

interface Settings {
  colors: [string, string];
  backgroundColor: string;
  shape: PhotonKnotShape;
  size: number;
  offsetX: number;
  offsetY: number;
  ribbonWidth: number;
  loop: number;
  twists: number;
  flowSpeed: number;
  rotationSpeed: number;
  tilt: number;
  glow: number;
  edges: number;
  folds: number;
  sheen: number;
  threads: number;
  depth: number;
  parallax: number;
  interactive: boolean;
  draggable: boolean;
  flip: number;
  bloom: number;
  grain: number;
  intro: boolean;
  paused: boolean;
  dpr: number;
  reduced: boolean;
}

interface Controller {
  sync: () => void;
  flip: (position?: number, strength?: number) => void;
  spin: (velocity?: number) => void;
  replay: () => void;
  destroy: () => void;
}

interface Wave {
  at: number;
  born: number;
  strength: number;
}

const KNOTS: Record<PhotonKnotShape, [number, number]> = {
  trefoil: [2, 3],
  cinquefoil: [2, 5],
  septafoil: [2, 7],
  star: [3, 5],
  tangle: [3, 4],
  halo: [1, 0],
};
const SHAPES = Object.keys(KNOTS) as PhotonKnotShape[];
const MAX_WAVES = 6;
const WAVE_LIFE = 9;
const FOV = 30;
const ACROSS = 24;
const SAMPLES = 720;
const INTRO = 2.6;

const clamp = (value: number, min: number, max: number) =>
  Math.min(max, Math.max(min, value));
const finite = (value: number, fallback: number) =>
  Number.isFinite(value) ? value : fallback;

const scratchColor = new THREE.Color();

const parseColor = (value: string): [number, number, number] | null => {
  const text = value.trim().toLowerCase();
  if (!text || text === "transparent" || text === "none" || text === "auto") return null;
  try {
    scratchColor.setStyle(text, THREE.NoColorSpace);
  } catch {
    return null;
  }
  return [scratchColor.r, scratchColor.g, scratchColor.b];
};

const toLinear = (c: number) => (c <= 0.04045 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4));

const toOklab = ([r, g, b]: [number, number, number]): [number, number, number] => {
  const lr = toLinear(r);
  const lg = toLinear(g);
  const lb = toLinear(b);
  const l = Math.cbrt(0.4122214708 * lr + 0.5363325363 * lg + 0.0514459929 * lb);
  const m = Math.cbrt(0.2119034982 * lr + 0.6806995451 * lg + 0.1073969566 * lb);
  const s = Math.cbrt(0.0883024619 * lr + 0.2817188376 * lg + 0.6299787005 * lb);
  return [
    0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s,
    1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s,
    0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s,
  ];
};

const subscribeToMotion = (notify: () => void) => {
  const query = window.matchMedia("(prefers-reduced-motion: reduce)");
  query.addEventListener("change", notify);
  return () => query.removeEventListener("change", notify);
};

const readMotion = () =>
  window.matchMedia("(prefers-reduced-motion: reduce)").matches;

const ribbonVertex = `
precision highp float;
uniform mat4 modelMatrix;
uniform mat4 modelViewMatrix;
uniform mat4 projectionMatrix;
uniform float uP;
uniform float uQ;
uniform float uLoop;
uniform float uWidth;
uniform float uTwists;
uniform float uPhase;
uniform float uTime;
uniform float uMotion;
uniform float uReveal;
uniform vec4 uWaves[${MAX_WAVES}];
in vec3 position;
out vec3 vNormal;
out vec3 vTangent;
out vec3 vWorld;
out vec2 vUv;
out float vDepth;
out float vShown;

const float TAU = 6.28318530718;
const float PI = 3.14159265359;

vec3 knot(float t) {
  float rho = 1.0 + uLoop * cos(uQ * t);
  return vec3(rho * cos(uP * t), rho * sin(uP * t), uLoop * sin(uQ * t));
}

vec3 knotTangent(float t) {
  float cq = cos(uQ * t);
  float sq = sin(uQ * t);
  float cp = cos(uP * t);
  float sp = sin(uP * t);
  float rho = 1.0 + uLoop * cq;
  float drho = -uLoop * uQ * sq;
  return vec3(drho * cp - rho * uP * sp, drho * sp + rho * uP * cp, uLoop * uQ * cq);
}

float twistAt(float u) {
  float angle = PI * uTwists * u + uPhase + 0.5 * uMotion * sin(u * TAU * 2.0 + uTime * 0.15);
  for (int i = 0; i < ${MAX_WAVES}; i++) {
    vec4 wave = uWaves[i];
    if (wave.z <= 0.0) continue;
    float travel = wave.y * 0.09;
    float ahead = fract(u - wave.x - travel + 0.5) - 0.5;
    float behind = fract(u - wave.x + travel + 0.5) - 0.5;
    float envelope = wave.z * smoothstep(0.0, 0.35, wave.y) * exp(-wave.y / 2.4);
    angle += PI * envelope * (exp(-ahead * ahead / 0.004) + exp(-behind * behind / 0.004));
  }
  return angle;
}

void main() {
  float u = position.x;
  float v = position.y;
  float t = u * TAU;
  vec3 center = knot(t);
  vec3 tangent = normalize(knotTangent(t));
  vec3 ring = vec3(cos(uP * t) * cos(uQ * t), sin(uP * t) * cos(uQ * t), sin(uQ * t));
  ring = normalize(ring - dot(ring, tangent) * tangent);
  vec3 side = cross(tangent, ring);
  float angle = twistAt(u);
  vec3 across = cos(angle) * ring + sin(angle) * side;
  float closing = smoothstep(0.88, 1.0, uReveal);
  float head = mix(smoothstep(uReveal, uReveal - 0.06, u), 1.0, closing);
  float tail = mix(smoothstep(0.0, 0.06, u), 1.0, closing);
  float drawn = uReveal >= 0.9999 ? 1.0 : head * tail;
  float breath = 1.0 - 0.2 * uMotion + 0.2 * uMotion * sin(u * TAU * 3.0 + uTime * 0.13);
  vec3 point = center + across * v * uWidth * breath * drawn;
  vec4 world = modelMatrix * vec4(point, 1.0);
  vWorld = world.xyz;
  vNormal = normalize(mat3(modelMatrix) * cross(tangent, across));
  vTangent = normalize(mat3(modelMatrix) * tangent);
  vUv = vec2(u, v);
  vShown = uReveal >= 0.9999 ? 1.0 : step(u, uReveal + 0.08 * closing);
  vec4 view = modelViewMatrix * vec4(point, 1.0);
  vDepth = -view.z;
  gl_Position = projectionMatrix * view;
}
`;

const ribbonFragment = `
precision highp float;
uniform vec3 cameraPosition;
uniform vec3 uLabA;
uniform vec3 uLabMid;
uniform vec3 uLabB;
uniform vec3 uLightDir;
uniform float uGlow;
uniform float uEdge;
uniform float uFold;
uniform float uSheen;
uniform float uThreads;
uniform float uDepth;
uniform float uFocus;
uniform float uFlow;
in vec3 vNormal;
in vec3 vTangent;
in vec3 vWorld;
in vec2 vUv;
in float vDepth;
in float vShown;
out vec4 fragColor;

const float TAU = 6.28318530718;

vec3 fromLab(vec3 lab) {
  vec3 lms = mat3(1.0, 1.0, 1.0, 0.3963377774, -0.1055613458, -0.0894841775, 0.2158037573, -0.0638541728, -1.291485548) * lab;
  lms = lms * lms * lms;
  return max(mat3(4.0767416621, -1.2684380046, -0.0041960863, -3.3077115913, 2.6097574011, -0.7034186147, 0.2309699292, -0.3413193965, 1.707614701) * lms, 0.0);
}

void main() {
  if (vShown < 0.5) discard;
  vec3 view = normalize(cameraPosition - vWorld);
  vec3 normal = normalize(vNormal);
  float facing = abs(dot(normal, view));
  float offset = vDepth - uFocus;
  float blur = clamp(abs(offset) * 0.35, 0.0, 1.0);
  float back = smoothstep(-0.4, 0.9, offset);
  float thick = min(1.0 / max(facing, 0.012), mix(uFold, 3.0, blur));
  float across = abs(vUv.y);
  float pixel = fwidth(vUv.y);
  float edgeWidth = max(mix(1.4 * pixel, 0.22, blur), 0.0001);
  float rim = exp(-pow((1.0 - across) / edgeWidth, 2.0)) * min(1.0, 2.0 * pixel / edgeWidth + 0.15);
  float body = 1.0 - smoothstep(1.0 - max(edgeWidth * 2.0, 0.03), 1.0, across);
  float along = 0.5 + 0.5 * cos(vUv.x * TAU * 2.0 + uFlow);
  vec3 lab = along < 0.5 ? mix(uLabA, uLabMid, along * 2.0) : mix(uLabMid, uLabB, along * 2.0 - 1.0);
  vec3 tint = fromLab(lab);
  float weave = uThreads * (1.0 - blur) * (1.0 - smoothstep(0.02, 0.05, pixel));
  float threads = mix(1.0, 0.65 + 0.35 * pow(0.5 + 0.5 * cos(vUv.y * 64.0), 4.0), weave);
  vec3 halfway = normalize(uLightDir + view);
  float slope = dot(normalize(vTangent), halfway);
  float sheen = pow(max(1.0 - slope * slope, 0.0), 320.0) * uSheen * body * (0.4 + 0.6 * across);
  vec3 bright = fromLab(vec3(max(lab.x, 0.86), lab.yz * 0.75));
  float layer = body * threads * (0.22 + 0.78 * across * across);
  vec3 color = tint * (layer * thick * uGlow + rim * uEdge * min(thick, 3.0)) + bright * sheen;
  color *= 1.0 - back * uDepth;
  fragColor = vec4(color, 1.0);
}
`;

const passVertex = `
in vec3 position;
out vec2 vUv;
void main() {
  vUv = position.xy * 0.5 + 0.5;
  gl_Position = vec4(position.xy, 0.0, 1.0);
}
`;

const downFragment = `
precision highp float;
uniform sampler2D uSource;
uniform vec2 uTexel;
uniform float uThreshold;
in vec2 vUv;
out vec4 fragColor;
vec3 tap(vec2 d) { return texture(uSource, vUv + d * uTexel).rgb; }
void main() {
  vec3 a = tap(vec2(-2.0, 2.0));
  vec3 b = tap(vec2(0.0, 2.0));
  vec3 c = tap(vec2(2.0, 2.0));
  vec3 d = tap(vec2(-2.0, 0.0));
  vec3 e = tap(vec2(0.0, 0.0));
  vec3 f = tap(vec2(2.0, 0.0));
  vec3 g = tap(vec2(-2.0, -2.0));
  vec3 h = tap(vec2(0.0, -2.0));
  vec3 i = tap(vec2(2.0, -2.0));
  vec3 j = tap(vec2(-1.0, 1.0));
  vec3 k = tap(vec2(1.0, 1.0));
  vec3 l = tap(vec2(-1.0, -1.0));
  vec3 m = tap(vec2(1.0, -1.0));
  vec3 color = e * 0.125 + (a + c + g + i) * 0.03125 + (b + d + f + h) * 0.0625 + (j + k + l + m) * 0.125;
  if (uThreshold > 0.0) {
    float peak = max(color.r, max(color.g, color.b));
    float knee = 0.6;
    float soft = clamp(peak - uThreshold + knee, 0.0, 2.0 * knee);
    soft = soft * soft / (4.0 * knee + 0.00001);
    color *= max(soft, peak - uThreshold) / max(peak, 0.00001);
  }
  fragColor = vec4(color, 1.0);
}
`;

const upFragment = `
precision highp float;
uniform sampler2D uSource;
uniform vec2 uTexel;
in vec2 vUv;
out vec4 fragColor;
vec3 tap(vec2 d) { return texture(uSource, vUv + d * uTexel).rgb; }
void main() {
  vec3 color = tap(vec2(0.0)) * 4.0
    + (tap(vec2(-1.0, 0.0)) + tap(vec2(1.0, 0.0)) + tap(vec2(0.0, -1.0)) + tap(vec2(0.0, 1.0))) * 2.0
    + tap(vec2(-1.0, -1.0)) + tap(vec2(1.0, -1.0)) + tap(vec2(-1.0, 1.0)) + tap(vec2(1.0, 1.0));
  fragColor = vec4(color / 16.0, 1.0);
}
`;

const compositeFragment = `
precision highp float;
uniform sampler2D uScene;
uniform sampler2D uBloom;
uniform vec3 uBackground;
uniform float uBloomStrength;
uniform float uGrain;
uniform float uTime;
uniform float uInk;
in vec2 vUv;
out vec4 fragColor;

vec3 neutral(vec3 color) {
  const float start = 0.76;
  const float desaturation = 0.15;
  float x = min(color.r, min(color.g, color.b));
  float offset = x < 0.08 ? x - 6.25 * x * x : 0.04;
  color -= offset;
  float peak = max(color.r, max(color.g, color.b));
  if (peak < start) return color;
  const float d = 1.0 - start;
  float next = 1.0 - d * d / (peak + d - start);
  color *= next / peak;
  float g = 1.0 - 1.0 / (desaturation * (peak - next) + 1.0);
  return mix(color, vec3(next), g);
}

vec3 encode(vec3 c) {
  c = max(c, 0.0);
  return mix(c * 12.92, 1.055 * pow(c, vec3(1.0 / 2.4)) - 0.055, step(0.0031308, c));
}

float hash(vec2 p) {
  vec3 p3 = fract(vec3(p.xyx) * 0.1031);
  p3 += dot(p3, p3.yzx + 33.33);
  return fract((p3.x + p3.y) * p3.z);
}

void main() {
  vec3 emission = texture(uScene, vUv).rgb + texture(uBloom, vUv).rgb * uBloomStrength;
  float n = hash(gl_FragCoord.xy + fract(uTime * 13.0) * 911.0) - 0.5;
  float dither = (hash(gl_FragCoord.xy * 1.7 + 0.5) - 0.5) / 255.0;
  if (uInk > 0.5) {
    float peak = max(emission.r, max(emission.g, emission.b));
    vec3 hue = emission / max(peak, 0.0001);
    float cover = 1.0 - exp(-peak * 1.6);
    vec3 color = mix(uBackground, encode(hue * 0.62), cover);
    color += (n * uGrain * 0.06 + dither) * smoothstep(0.0, 0.05, cover);
    fragColor = vec4(clamp(color, 0.0, 1.0), 1.0);
    return;
  }
  vec3 light = encode(neutral(max(emission, 0.0)));
  vec3 color = 1.0 - (1.0 - uBackground) * (1.0 - light);
  float mask = smoothstep(0.0, 0.04, max(light.r, max(light.g, light.b)));
  color += (n * uGrain * 0.1 + dither) * mask;
  fragColor = vec4(clamp(color, 0.0, 1.0), 1.0);
}
`;

function createKnot(
  root: HTMLDivElement,
  stage: HTMLDivElement,
  settingsRef: { current: Settings },
): Controller | null {
  const doc = root.ownerDocument;
  const view = doc.defaultView ?? window;
  const canvas = doc.createElement("canvas");
  canvas.setAttribute("aria-hidden", "true");
  canvas.style.cssText = "position:absolute;inset:0;width:100%;height:100%;display:block";
  let renderer: THREE.WebGLRenderer;
  try {
    renderer = new THREE.WebGLRenderer({
      canvas,
      alpha: false,
      antialias: false,
      powerPreference: "high-performance",
    });
  } catch {
    return null;
  }
  stage.appendChild(canvas);
  renderer.autoClear = false;

  const settings = () => settingsRef.current;
  const floatTargets =
    renderer.extensions.has("EXT_color_buffer_float") ||
    renderer.extensions.has("EXT_color_buffer_half_float");
  const targetType = floatTargets ? THREE.HalfFloatType : THREE.UnsignedByteType;

  const passCamera = new THREE.OrthographicCamera(-1, 1, 1, -1, -1, 1);
  const passGeometry = new THREE.BufferGeometry();
  passGeometry.setAttribute(
    "position",
    new THREE.Float32BufferAttribute([-1, -1, 0, 3, -1, 0, -1, 3, 0], 3),
  );
  const passScene = new THREE.Scene();
  const pass = new THREE.Mesh(passGeometry);
  pass.frustumCulled = false;
  passScene.add(pass);

  const waveSlots = Array.from({ length: MAX_WAVES }, () => new THREE.Vector4(0, 0, 0, 0));
  const ribbonUniforms = {
    uP: { value: 2 },
    uQ: { value: 3 },
    uLoop: { value: 0.42 },
    uWidth: { value: 0.34 },
    uTwists: { value: 4 },
    uPhase: { value: 0 },
    uTime: { value: 0 },
    uMotion: { value: 1 },
    uReveal: { value: 1 },
    uWaves: { value: waveSlots },
    uLabA: { value: new THREE.Vector3() },
    uLabMid: { value: new THREE.Vector3() },
    uLabB: { value: new THREE.Vector3() },
    uLightDir: { value: new THREE.Vector3(-0.5, 0.7, 0.9).normalize() },
    uGlow: { value: 0.07 },
    uEdge: { value: 1.1 },
    uFold: { value: 14 },
    uSheen: { value: 0.15 },
    uThreads: { value: 0 },
    uDepth: { value: 0.6 },
    uFocus: { value: 6 },
    uFlow: { value: 0 },
  };
  const ribbonMaterial = new THREE.RawShaderMaterial({
    glslVersion: THREE.GLSL3,
    vertexShader: ribbonVertex,
    fragmentShader: ribbonFragment,
    uniforms: ribbonUniforms,
    blending: THREE.CustomBlending,
    blendEquation: THREE.AddEquation,
    blendSrc: THREE.OneFactor,
    blendDst: THREE.OneFactor,
    transparent: true,
    depthTest: false,
    depthWrite: false,
    side: THREE.DoubleSide,
  });
  let ribbonGeometry = new THREE.BufferGeometry();
  const ribbon = new THREE.Mesh(ribbonGeometry, ribbonMaterial);
  ribbon.frustumCulled = false;
  const scene = new THREE.Scene();
  scene.add(ribbon);
  const camera = new THREE.PerspectiveCamera(FOV, 1, 0.05, 100);

  const target = (samples: number) =>
    new THREE.WebGLRenderTarget(1, 1, {
      type: targetType,
      format: THREE.RGBAFormat,
      depthBuffer: false,
      stencilBuffer: false,
      minFilter: THREE.LinearFilter,
      magFilter: THREE.LinearFilter,
      generateMipmaps: false,
      samples,
    });
  const sceneTarget = target(4);
  const bloomTargets = Array.from({ length: 6 }, () => target(0));
  const background = new THREE.Vector3(0.04, 0.04, 0.04);
  const passMaterial = (
    fragmentShader: string,
    uniforms: Record<string, THREE.IUniform>,
    additive = false,
  ) =>
    new THREE.RawShaderMaterial({
      glslVersion: THREE.GLSL3,
      vertexShader: passVertex,
      fragmentShader,
      uniforms,
      blending: additive ? THREE.AdditiveBlending : THREE.NoBlending,
      transparent: additive,
      depthTest: false,
      depthWrite: false,
    });
  const downMaterial = passMaterial(downFragment, {
    uSource: { value: null },
    uTexel: { value: new THREE.Vector2() },
    uThreshold: { value: 0 },
  });
  const upMaterial = passMaterial(
    upFragment,
    { uSource: { value: null }, uTexel: { value: new THREE.Vector2() } },
    true,
  );
  const compositeMaterial = passMaterial(compositeFragment, {
    uScene: { value: sceneTarget.texture },
    uBloom: { value: bloomTargets[0].texture },
    uBackground: { value: background },
    uBloomStrength: { value: 0.1 },
    uGrain: { value: 0.25 },
    uTime: { value: 0 },
    uInk: { value: 0 },
  });

  let width = 0;
  let height = 0;
  let dpr = 1;
  let quality = 1;
  let slowFrames = 0;
  let fastFrames = 0;
  let frameAverage = 1000 / 60;
  let sized = false;
  let geometryKey = "";
  let colorKey = "";
  let ink = false;
  let backgroundAt = -10;
  let clock = 0;
  let phase = 0;
  let flow = 0;
  let spin = 0.4;
  let tumbleX = 0;
  let tumbleY = 0;
  let tumbleVX = 0;
  let tumbleVY = 0;
  let parallaxX = 0;
  let parallaxY = 0;
  let parallaxTargetX = 0;
  let parallaxTargetY = 0;
  let lightX = -0.5;
  let lightY = 0.7;
  let lightTargetX: number | null = null;
  let lightTargetY = 0.7;
  let introStart = -1;
  let reveal = 1;
  let raf = 0;
  let last = 0;
  let visible = false;
  let destroyed = false;
  let lost = false;
  let drag: {
    id: number;
    x: number;
    y: number;
    lastX: number;
    lastY: number;
    time: number;
    moved: boolean;
  } | null = null;
  const waves: Wave[] = [];
  const point = new THREE.Vector3();

  const readPage = (): [number, number, number] => {
    let node: HTMLElement | null = root.parentElement;
    while (node) {
      const fillColor = view.getComputedStyle(node).backgroundColor;
      const match = fillColor.match(/rgba?\(([^)]+)\)/);
      if (match) {
        const parts = match[1].split(",").map((part) => Number.parseFloat(part));
        if (parts.length < 4 || parts[3] > 0.05)
          return [parts[0] / 255, parts[1] / 255, parts[2] / 255];
      }
      node = node.parentElement;
    }
    const text = view.getComputedStyle(root).color.match(/rgba?\(([^)]+)\)/);
    if (text) {
      const parts = text[1].split(",").map((part) => Number.parseFloat(part));
      return parts[0] + parts[1] + parts[2] > 382 ? [0.04, 0.04, 0.04] : [1, 1, 1];
    }
    return [0.04, 0.04, 0.04];
  };

  const syncColors = () => {
    const s = settings();
    const page = parseColor(s.backgroundColor) ?? readPage();
    const key = [page.join(","), s.colors[0], s.colors[1]].join("|");
    if (key === colorKey) return;
    colorKey = key;
    ink = 0.2126 * page[0] + 0.7152 * page[1] + 0.0722 * page[2] > 0.55;
    background.set(page[0], page[1], page[2]);
    const start = toOklab(parseColor(s.colors[0]) ?? [0.32, 0.15, 1]);
    const end = toOklab(parseColor(s.colors[1]) ?? [1, 0.62, 0.99]);
    ribbonUniforms.uLabA.value.set(start[0], start[1], start[2]);
    ribbonUniforms.uLabB.value.set(end[0], end[1], end[2]);
    ribbonUniforms.uLabMid.value.set(
      (start[0] + end[0]) / 2,
      (start[1] + end[1]) / 2,
      (start[2] + end[2]) / 2,
    );
    compositeMaterial.uniforms.uInk.value = ink ? 1 : 0;
  };

  const knotOf = (): [number, number] => {
    const s = settings();
    return KNOTS[SHAPES.includes(s.shape) ? s.shape : "trefoil"];
  };

  const syncGeometry = () => {
    const [p, q] = knotOf();
    const key = `${p}:${q}`;
    if (key === geometryKey) return;
    geometryKey = key;
    const segments = 700 * p + 160 * q + 200;
    const positions = new Float32Array((segments + 1) * (ACROSS + 1) * 3);
    let cursor = 0;
    for (let i = 0; i <= segments; i++) {
      for (let j = 0; j <= ACROSS; j++) {
        positions[cursor++] = i / segments;
        positions[cursor++] = (j / ACROSS) * 2 - 1;
        positions[cursor++] = 0;
      }
    }
    const indices = new Uint32Array(segments * ACROSS * 6);
    cursor = 0;
    for (let i = 0; i < segments; i++) {
      for (let j = 0; j < ACROSS; j++) {
        const a = i * (ACROSS + 1) + j;
        const b = a + ACROSS + 1;
        indices[cursor++] = a;
        indices[cursor++] = b;
        indices[cursor++] = a + 1;
        indices[cursor++] = a + 1;
        indices[cursor++] = b;
        indices[cursor++] = b + 1;
      }
    }
    const next = new THREE.BufferGeometry();
    next.setAttribute("position", new THREE.BufferAttribute(positions, 3));
    next.setIndex(new THREE.BufferAttribute(indices, 1));
    ribbonGeometry.dispose();
    ribbonGeometry = next;
    ribbon.geometry = next;
  };

  const applyQuality = () => {
    const deviceW = Math.max(1, Math.round(width * dpr * quality));
    const deviceH = Math.max(1, Math.round(height * dpr * quality));
    sceneTarget.setSize(deviceW, deviceH);
    let w = deviceW;
    let h = deviceH;
    for (const bloomTarget of bloomTargets) {
      w = Math.max(1, Math.round(w / 2));
      h = Math.max(1, Math.round(h / 2));
      bloomTarget.setSize(w, h);
    }
  };

  const readSize = () => {
    const s = settings();
    const nextWidth = Math.round(root.clientWidth);
    const nextHeight = Math.round(root.clientHeight);
    if (nextWidth < 2 || nextHeight < 2) {
      sized = false;
      return false;
    }
    const nextDpr = Math.min(view.devicePixelRatio || 1, clamp(finite(s.dpr, 2), 0.5, 3));
    if (sized && nextWidth === width && nextHeight === height && nextDpr === dpr) return false;
    sized = true;
    width = nextWidth;
    height = nextHeight;
    dpr = nextDpr;
    renderer.setPixelRatio(dpr);
    renderer.setSize(width, height, false);
    applyQuality();
    return true;
  };

  const adapt = (interval: number) => {
    frameAverage += (interval - frameAverage) * 0.08;
    if (frameAverage > 24) {
      slowFrames++;
      fastFrames = 0;
    } else if (frameAverage < 13) {
      fastFrames++;
      slowFrames = 0;
    } else {
      slowFrames = 0;
      fastFrames = 0;
    }
    if (slowFrames > 45 && quality > 0.6) {
      quality = Math.max(0.6, quality * 0.8);
      slowFrames = 0;
      frameAverage = 1000 / 60;
      applyQuality();
    } else if (fastFrames > 240 && quality < 1) {
      quality = Math.min(1, quality * 1.15);
      fastFrames = 0;
      applyQuality();
    }
  };

  const place = () => {
    const s = settings();
    const loop = clamp(finite(s.loop, 0.42), 0, 0.7);
    const ribbonWidth = clamp(finite(s.ribbonWidth, 0.34), 0.02, 0.8);
    const radius = 1 + loop + ribbonWidth;
    const aspect = width / Math.max(height, 1);
    const half = Math.tan((FOV * Math.PI) / 360);
    const fit = clamp(finite(s.size, 0.72), 0.2, 3) * Math.min(1, aspect);
    const distance = radius / (fit * half);
    camera.aspect = aspect;
    camera.position.set(0, 0, distance);
    camera.near = Math.max(0.05, distance - radius * 3);
    camera.far = distance + radius * 3;
    camera.updateProjectionMatrix();
    ribbon.position.set(
      clamp(finite(s.offsetX, 0), -1, 1) * 2 * distance * half * aspect,
      -clamp(finite(s.offsetY, 0), -1, 1) * 2 * distance * half,
      0,
    );
    const tilt = (clamp(finite(s.tilt, 32), -90, 90) * Math.PI) / 180;
    const motion = s.reduced || s.paused ? 0 : 1;
    ribbon.rotation.set(
      -tilt + tumbleX + parallaxY + 0.08 * motion * Math.sin(clock * 0.1),
      tumbleY + parallaxX + 0.15 * motion * Math.sin(clock * 0.07),
      spin,
      "XYZ",
    );
    ribbon.updateMatrixWorld();
    camera.updateMatrixWorld();
    ribbonUniforms.uFocus.value = distance;
    return { loop, ribbonWidth };
  };

  const applyUniforms = () => {
    const s = settings();
    const [p, q] = knotOf();
    const { loop, ribbonWidth } = place();
    const u = ribbonUniforms;
    u.uP.value = p;
    u.uQ.value = q;
    u.uLoop.value = loop;
    u.uWidth.value = ribbonWidth;
    u.uTwists.value = clamp(Math.round(finite(s.twists, 4)), 0, 12);
    u.uPhase.value = phase;
    u.uTime.value = clock;
    u.uMotion.value = s.reduced ? 0 : 1;
    u.uReveal.value = reveal;
    u.uFlow.value = flow;
    u.uGlow.value = clamp(finite(s.glow, 1), 0, 4) * 0.07;
    u.uEdge.value = clamp(finite(s.edges, 1), 0, 4) * 1.1;
    u.uFold.value = 3 + clamp(finite(s.folds, 1), 0, 3) * 11;
    u.uSheen.value = clamp(finite(s.sheen, 1), 0, 4) * 0.15;
    u.uThreads.value = clamp(finite(s.threads, 0), 0, 1);
    u.uDepth.value = clamp(finite(s.depth, 0.6), 0, 1);
    u.uLightDir.value.set(lightX, lightY, 0.9).normalize();
    for (let i = 0; i < MAX_WAVES; i++) {
      const wave = waves[i];
      if (wave) waveSlots[i].set(wave.at, clock - wave.born, wave.strength, 0);
      else waveSlots[i].set(0, 0, 0, 0);
    }
    compositeMaterial.uniforms.uGrain.value = clamp(finite(s.grain, 0.25), 0, 1);
    compositeMaterial.uniforms.uTime.value = clock;
  };

  const render = () => {
    if (!sized) return;
    const s = settings();
    if (performance.now() - backgroundAt > 1000) {
      backgroundAt = performance.now();
      syncColors();
    }
    syncGeometry();
    applyUniforms();
    renderer.setRenderTarget(sceneTarget);
    renderer.setClearColor(0x000000, 1);
    renderer.clear();
    renderer.render(scene, camera);
    const bloom = clamp(finite(s.bloom, 0.6), 0, 3) * (ink ? 0.5 : 1);
    if (bloom > 0.001) {
      let source: THREE.WebGLRenderTarget = sceneTarget;
      pass.material = downMaterial;
      for (const bloomTarget of bloomTargets) {
        downMaterial.uniforms.uThreshold.value = source === sceneTarget ? 0.9 : 0;
        downMaterial.uniforms.uSource.value = source.texture;
        downMaterial.uniforms.uTexel.value.set(1 / source.width, 1 / source.height);
        renderer.setRenderTarget(bloomTarget);
        renderer.render(passScene, passCamera);
        source = bloomTarget;
      }
      pass.material = upMaterial;
      for (let i = bloomTargets.length - 1; i > 0; i--) {
        upMaterial.uniforms.uSource.value = bloomTargets[i].texture;
        upMaterial.uniforms.uTexel.value.set(1 / bloomTargets[i].width, 1 / bloomTargets[i].height);
        renderer.setRenderTarget(bloomTargets[i - 1]);
        renderer.render(passScene, passCamera);
      }
    }
    compositeMaterial.uniforms.uBloomStrength.value = bloom / bloomTargets.length;
    pass.material = compositeMaterial;
    renderer.setRenderTarget(null);
    renderer.render(passScene, passCamera);
  };

  const positionAt = (clientX: number, clientY: number) => {
    const box = root.getBoundingClientRect();
    if (box.width <= 0 || box.height <= 0) return null;
    const sx = ((clientX - box.left) / box.width) * 2 - 1;
    const sy = 1 - ((clientY - box.top) / box.height) * 2;
    const [p, q] = knotOf();
    const { loop } = place();
    const aspect = box.width / box.height;
    let best = Infinity;
    let at = 0;
    for (let i = 0; i < SAMPLES; i++) {
      const t = (i / SAMPLES) * Math.PI * 2;
      const rho = 1 + loop * Math.cos(q * t);
      point.set(rho * Math.cos(p * t), rho * Math.sin(p * t), loop * Math.sin(q * t));
      point.applyMatrix4(ribbon.matrixWorld).project(camera);
      const distance = Math.hypot((point.x - sx) * aspect, point.y - sy) + point.z * 0.02;
      if (distance < best) {
        best = distance;
        at = i / SAMPLES;
      }
    }
    return at;
  };

  const addWave = (at: number, strength: number) => {
    if (waves.length >= MAX_WAVES) {
      let oldest = 0;
      for (let i = 1; i < waves.length; i++) if (waves[i].born < waves[oldest].born) oldest = i;
      if (clock - waves[oldest].born < 1.2) return;
      waves.splice(oldest, 1);
    }
    waves.push({ at, born: clock, strength });
  };

  const schedule = () => {
    if (destroyed || lost || !visible || raf) return;
    raf = requestAnimationFrame(frame);
  };

  const frame = (now: number) => {
    raf = 0;
    if (destroyed || lost) return;
    const s = settings();
    if (last && now - last < 100) adapt(now - last);
    const dt =
      last && now - last < 100 ? Math.min(Math.max((now - last) / 1000, 0), 0.05) : 1 / 60;
    last = now;
    const moving = !s.reduced && !s.paused;
    if (moving) {
      clock += dt;
      const speed = clamp(finite(s.flowSpeed, 0.5), 0, 3);
      phase += dt * speed * 0.4;
      flow += dt * speed * 0.36;
      spin += dt * clamp(finite(s.rotationSpeed, 0.5), -3, 3) * 0.09;
      if (!drag) {
        const stiffness = 2.2;
        const damping = 2.0;
        tumbleVX += (-stiffness * tumbleX - damping * tumbleVX) * dt;
        tumbleVY += (-stiffness * tumbleY - damping * tumbleVY) * dt;
        tumbleX += tumbleVX * dt;
        tumbleY += tumbleVY * dt;
      }
    }
    if (s.intro && !s.reduced) {
      if (introStart < 0) introStart = now;
      const k = clamp((now - introStart) / 1000 / INTRO, 0, 1);
      reveal = k >= 1 ? 1 : k < 0.5 ? 4 * k * k * k : 1 - Math.pow(-2 * k + 2, 3) / 2;
    } else {
      reveal = 1;
    }
    const follow = s.reduced ? 1 : 1 - Math.exp(-dt / 0.5);
    parallaxX += (parallaxTargetX - parallaxX) * follow;
    parallaxY += (parallaxTargetY - parallaxY) * follow;
    const aimX = lightTargetX ?? -0.5 + 0.3 * Math.sin(clock * 0.09);
    const aimY = lightTargetX === null ? 0.7 : lightTargetY;
    const glide = s.reduced ? 1 : 1 - Math.exp(-dt * 2.5);
    lightX += (aimX - lightX) * glide;
    lightY += (aimY - lightY) * glide;
    for (let i = waves.length - 1; i >= 0; i--)
      if (clock - waves[i].born > WAVE_LIFE) waves.splice(i, 1);
    render();
    const settling =
      Math.abs(parallaxTargetX - parallaxX) > 0.0001 ||
      Math.abs(parallaxTargetY - parallaxY) > 0.0001 ||
      Math.abs(aimX - lightX) + Math.abs(aimY - lightY) > 0.0005 ||
      Math.abs(tumbleX) + Math.abs(tumbleY) + Math.abs(tumbleVX) + Math.abs(tumbleVY) > 0.0005;
    const animating = moving || settling || reveal < 1 || drag !== null;
    if (animating && !doc.hidden) raf = requestAnimationFrame(frame);
    else last = 0;
  };

  const isControl = (target: EventTarget | null) => {
    const element = target as Element | null;
    return (
      !!element &&
      typeof element.closest === "function" &&
      !!element.closest("a,button,input,textarea,select,label,summary,[role=button],[contenteditable=true]")
    );
  };

  const onPointerDown = (event: PointerEvent) => {
    const s = settings();
    if (!event.isPrimary || isControl(event.target)) return;
    if (event.pointerType === "mouse" && event.button !== 0) return;
    if (!s.draggable && !s.interactive) return;
    drag = {
      id: event.pointerId,
      x: event.clientX,
      y: event.clientY,
      lastX: event.clientX,
      lastY: event.clientY,
      time: performance.now(),
      moved: false,
    };
    tumbleVX = 0;
    tumbleVY = 0;
    schedule();
  };

  const onPointerMove = (event: PointerEvent) => {
    const s = settings();
    if (drag && event.pointerId === drag.id) {
      const dx = event.clientX - drag.lastX;
      const dy = event.clientY - drag.lastY;
      if (!drag.moved && Math.hypot(event.clientX - drag.x, event.clientY - drag.y) > 6) {
        drag.moved = true;
        if (s.draggable) {
          try {
            root.setPointerCapture(event.pointerId);
          } catch {
            drag.moved = true;
          }
        }
      }
      if (drag.moved && s.draggable) {
        const now = performance.now();
        const span = Math.max(root.clientHeight, 1);
        const turnX = (dx / span) * Math.PI * 1.2;
        const turnY = (dy / span) * Math.PI * 1.2;
        tumbleY += turnX;
        tumbleX = clamp(tumbleX + turnY, -1.4, 1.4);
        const elapsed = Math.max((now - drag.time) / 1000, 0.008);
        tumbleVY = s.reduced ? 0 : clamp(turnX / elapsed, -8, 8);
        tumbleVX = s.reduced ? 0 : clamp(turnY / elapsed, -8, 8);
        drag.time = now;
      }
      drag.lastX = event.clientX;
      drag.lastY = event.clientY;
      schedule();
      return;
    }
    if (event.pointerType === "touch") return;
    const box = root.getBoundingClientRect();
    if (box.width <= 0 || box.height <= 0) return;
    const x = (event.clientX - box.left) / box.width - 0.5;
    const y = (event.clientY - box.top) / box.height - 0.5;
    const amount = clamp(finite(s.parallax, 0.5), 0, 2);
    parallaxTargetX = x * 0.44 * amount;
    parallaxTargetY = y * 0.32 * amount;
    if (s.interactive) {
      lightTargetX = x * 2.2;
      lightTargetY = -y * 2.2;
    }
    schedule();
  };

  const onPointerUp = (event: PointerEvent) => {
    const current = drag;
    if (!current || event.pointerId !== current.id) return;
    drag = null;
    if (root.hasPointerCapture(event.pointerId)) root.releasePointerCapture(event.pointerId);
    if (Math.abs(performance.now() - current.time) > 120) {
      tumbleVX *= 0.2;
      tumbleVY *= 0.2;
    }
    const s = settings();
    if (!current.moved && s.interactive && !s.reduced && !s.paused) {
      const at = positionAt(event.clientX, event.clientY);
      if (at !== null) addWave(at, clamp(finite(s.flip, 1), 0, 2));
    }
    schedule();
  };

  const onPointerCancel = (event: PointerEvent) => {
    if (drag && event.pointerId === drag.id) drag = null;
    schedule();
  };

  const onPointerLeave = (event: PointerEvent) => {
    if (event.pointerType === "touch") return;
    parallaxTargetX = 0;
    parallaxTargetY = 0;
    lightTargetX = null;
    schedule();
  };

  const onVisibility = () => {
    last = 0;
    schedule();
  };

  const onLost = (event: Event) => {
    event.preventDefault();
    lost = true;
    cancelAnimationFrame(raf);
    raf = 0;
  };

  const onRestored = () => {
    lost = false;
    colorKey = "";
    geometryKey = "";
    schedule();
  };

  const resizeObserver = new ResizeObserver(() => {
    if (readSize()) {
      render();
      schedule();
    }
  });

  const intersection = new IntersectionObserver(
    ([entry]) => {
      visible = entry.isIntersecting;
      last = 0;
      schedule();
    },
    { rootMargin: "120px" },
  );

  root.addEventListener("pointerdown", onPointerDown);
  root.addEventListener("pointermove", onPointerMove);
  root.addEventListener("pointerup", onPointerUp);
  root.addEventListener("pointercancel", onPointerCancel);
  root.addEventListener("pointerleave", onPointerLeave);
  doc.addEventListener("visibilitychange", onVisibility);
  canvas.addEventListener("webglcontextlost", onLost);
  canvas.addEventListener("webglcontextrestored", onRestored);
  resizeObserver.observe(root);
  intersection.observe(root);
  if (settings().intro && !settings().reduced) reveal = 0;
  readSize();
  syncColors();

  return {
    sync: () => {
      backgroundAt = -10;
      readSize();
      last = 0;
      if (!raf && visible) {
        render();
        schedule();
      }
    },
    flip: (position, strength) => {
      const s = settings();
      if (s.reduced) return;
      addWave(
        position === undefined ? Math.random() : ((position % 1) + 1) % 1,
        clamp(finite(strength ?? s.flip, 1), 0, 2),
      );
      schedule();
    },
    spin: (velocity) => {
      if (settings().reduced) return;
      tumbleVY = clamp(finite(velocity ?? 4, 4), -8, 8);
      schedule();
    },
    replay: () => {
      if (settings().reduced) return;
      introStart = -1;
      reveal = 0;
      last = 0;
      schedule();
    },
    destroy: () => {
      destroyed = true;
      cancelAnimationFrame(raf);
      resizeObserver.disconnect();
      intersection.disconnect();
      root.removeEventListener("pointerdown", onPointerDown);
      root.removeEventListener("pointermove", onPointerMove);
      root.removeEventListener("pointerup", onPointerUp);
      root.removeEventListener("pointercancel", onPointerCancel);
      root.removeEventListener("pointerleave", onPointerLeave);
      doc.removeEventListener("visibilitychange", onVisibility);
      canvas.removeEventListener("webglcontextlost", onLost);
      canvas.removeEventListener("webglcontextrestored", onRestored);
      ribbonGeometry.dispose();
      ribbonMaterial.dispose();
      passGeometry.dispose();
      downMaterial.dispose();
      upMaterial.dispose();
      compositeMaterial.dispose();
      sceneTarget.dispose();
      for (const bloomTarget of bloomTargets) bloomTarget.dispose();
      renderer.dispose();
      canvas.remove();
    },
  };
}

export const PhotonKnot = forwardRef<PhotonKnotHandle, PhotonKnotProps>(function PhotonKnot(
  {
    colors = ["#5227FF", "#FF9FFC"],
    backgroundColor = "transparent",
    shape = "trefoil",
    size = 0.72,
    offsetX = 0,
    offsetY = 0,
    ribbonWidth = 0.34,
    loop = 0.42,
    twists = 4,
    flowSpeed = 0.5,
    rotationSpeed = 0.5,
    tilt = 32,
    glow = 1,
    edges = 1,
    folds = 1,
    sheen = 1,
    threads = 0,
    depth = 0.6,
    parallax = 0.5,
    interactive = true,
    draggable = true,
    flip = 1,
    bloom = 0.6,
    grain = 0.25,
    intro = true,
    paused = false,
    dpr = 2,
    children,
    className,
    style,
  },
  ref,
) {
  const rootRef = useRef<HTMLDivElement>(null);
  const stageRef = useRef<HTMLDivElement>(null);
  const controllerRef = useRef<Controller | null>(null);
  const reduced = useSyncExternalStore(subscribeToMotion, readMotion, () => false);

  const settings: Settings = {
    colors,
    backgroundColor,
    shape,
    size,
    offsetX,
    offsetY,
    ribbonWidth,
    loop,
    twists,
    flowSpeed,
    rotationSpeed,
    tilt,
    glow,
    edges,
    folds,
    sheen,
    threads,
    depth,
    parallax,
    interactive,
    draggable,
    flip,
    bloom,
    grain,
    intro,
    paused,
    dpr,
    reduced,
  };
  const settingsRef = useRef(settings);

  useEffect(() => {
    settingsRef.current = settings;
    controllerRef.current?.sync();
  });

  useEffect(() => {
    const root = rootRef.current;
    const stage = stageRef.current;
    if (!root || !stage) return;
    const controller = createKnot(root, stage, settingsRef);
    controllerRef.current = controller;
    return () => {
      controller?.destroy();
      controllerRef.current = null;
    };
  }, []);

  useImperativeHandle(
    ref,
    () => ({
      flip: (position?: number, strength?: number) =>
        controllerRef.current?.flip(position, strength),
      spin: (velocity?: number) => controllerRef.current?.spin(velocity),
      replay: () => controllerRef.current?.replay(),
    }),
    [],
  );

  const backgroundValue = backgroundColor.trim().toLowerCase();
  const transparent = backgroundValue === "transparent" || backgroundValue === "";

  return (
    <div
      ref={rootRef}
      className={["photon-knot", className].filter(Boolean).join(" ")}
      style={{
        backgroundColor: transparent ? undefined : backgroundColor,
        touchAction: "pan-y",
        cursor: draggable ? "grab" : undefined,
        ...style,
      }}
    >
      <div
        ref={stageRef}
        aria-hidden="true"
        style={{ position: "absolute", inset: 0, pointerEvents: "none" }}
      />
      {children ? (
        <div style={{ position: "relative", zIndex: 1, height: "100%" }}>{children}</div>
      ) : null}
    </div>
  );
});

PhotonKnot.displayName = "PhotonKnot";

export default PhotonKnot;
