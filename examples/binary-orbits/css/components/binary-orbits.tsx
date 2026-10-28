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
import "./binary-orbits.css";

export type BinaryOrbitsMode = "auto" | "glow" | "ink";

export interface BinaryOrbitsHandle {
  ripple: () => void;
}

export interface BinaryOrbitsProps {
  colors?: [string, string];
  backgroundColor?: string;
  mode?: BinaryOrbitsMode;
  stars?: number;
  starSize?: number;
  sparkle?: number;
  arms?: number;
  twist?: number;
  armStrength?: number;
  dust?: number;
  core?: number;
  coreSize?: number;
  innerVoid?: number;
  thickness?: number;
  tilt?: number;
  roll?: number;
  scale?: number;
  centerX?: number;
  centerY?: number;
  speed?: number;
  twinkle?: number;
  depth?: number;
  glow?: number;
  intensity?: number;
  interactive?: boolean;
  hoverWake?: boolean;
  clickRipple?: boolean;
  wakeStrength?: number;
  rippleStrength?: number;
  hoverBoost?: number;
  paused?: boolean;
  quality?: number;
  className?: string;
  style?: CSSProperties;
  children?: ReactNode;
}

interface Settings {
  colors: [string, string];
  backgroundColor: string;
  mode: BinaryOrbitsMode;
  stars: number;
  starSize: number;
  sparkle: number;
  arms: number;
  twist: number;
  armStrength: number;
  dust: number;
  core: number;
  coreSize: number;
  innerVoid: number;
  thickness: number;
  tilt: number;
  roll: number;
  scale: number;
  centerX: number;
  centerY: number;
  speed: number;
  twinkle: number;
  depth: number;
  glow: number;
  intensity: number;
  interactive: boolean;
  hoverWake: boolean;
  clickRipple: boolean;
  wakeStrength: number;
  rippleStrength: number;
  hoverBoost: number;
  paused: boolean;
  quality: number;
  reduced: boolean;
}

interface Controller {
  sync: () => void;
  destroy: () => void;
  ripple: () => void;
}

const STRIDE = 512;
const RIPPLES = 6;
const DISTANCE = 3.6;
const RELAX = 3.5;
const RING_LIFE = 7;
const WAKE_WIDTH = 0.09;
const TAU = Math.PI * 2;

const subscribeToMotion = (notify: () => void) => {
  const media = window.matchMedia("(prefers-reduced-motion: reduce)");
  media.addEventListener("change", notify);
  return () => media.removeEventListener("change", notify);
};

const readMotion = () =>
  typeof window !== "undefined" &&
  window.matchMedia("(prefers-reduced-motion: reduce)").matches;

const clamp = (value: number, min: number, max: number) =>
  Math.min(max, Math.max(min, value));

let colorContext: CanvasRenderingContext2D | null = null;

const parseColor = (value: string, fallback: [number, number, number]): [number, number, number] => {
  if (!colorContext) colorContext = document.createElement("canvas").getContext("2d");
  if (!colorContext) return fallback;
  colorContext.fillStyle = "#010203";
  colorContext.fillStyle = value;
  const read = String(colorContext.fillStyle);
  if (read === "#010203" && value.trim().toLowerCase() !== "#010203") return fallback;
  if (read.startsWith("#")) {
    const hex = read.slice(1);
    return [
      parseInt(hex.slice(0, 2), 16) / 255,
      parseInt(hex.slice(2, 4), 16) / 255,
      parseInt(hex.slice(4, 6), 16) / 255,
    ];
  }
  const parts = read.match(/[\d.]+/g);
  if (!parts || parts.length < 3) return fallback;
  return [Number(parts[0]) / 255, Number(parts[1]) / 255, Number(parts[2]) / 255];
};

const toLinear = (value: number) => (value <= 0.04045 ? value / 12.92 : Math.pow((value + 0.055) / 1.055, 2.4));

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

const orbitChunk = `
uniform float uTime;
uniform float uSpin;
uniform float uCoreRadius;
uniform float uArms;
uniform float uTwist;
uniform float uEps;
uniform float uPattern;

float omega(float a) {
  return uSpin / sqrt(a * a + uCoreRadius * uCoreRadius);
}

vec3 orbit(float a, float psi, float bulge, out float phase) {
  float th = psi + omega(a) * uTime;
  phase = uArms * (th + uTwist * log(max(a, 1e-3)) - uPattern * uTime);
  float r = a * (1.0 - uEps * cos(phase) * (1.0 - bulge));
  return vec3(r * cos(th), r * sin(th), th);
}
`;

const stateFragment = `
precision highp float;
uniform sampler2D uStatic;
uniform sampler2D uState;
uniform vec4 uSegment;
uniform float uGather;
uniform float uDrag;
uniform float uWidth;
uniform float uDecay;
${orbitChunk}
out vec4 fragColor;

void main() {
  ivec2 cell = ivec2(gl_FragCoord.xy);
  vec2 offset = texelFetch(uState, cell, 0).xy;
  if (uGather > 0.0) {
    vec4 star = texelFetch(uStatic, cell, 0);
    float phase;
    vec3 at = orbit(star.x, star.y, star.z, phase);
    vec2 radial = vec2(cos(at.z), sin(at.z));
    vec2 tangent = vec2(-radial.y, radial.x);
    vec2 p = at.xy + radial * offset.x + tangent * offset.y;
    vec2 d = uSegment.zw - uSegment.xy;
    float len = length(d);
    vec2 dir = len > 1e-6 ? d / len : vec2(1.0, 0.0);
    float along = clamp(dot(p - uSegment.xy, dir), 0.0, len);
    vec2 n = p - uSegment.xy - dir * along;
    float w = exp(-dot(n, n) / (uWidth * uWidth)) * (1.0 - 0.7 * star.z) * smoothstep(0.05, 0.3, star.x);
    vec2 delta = (dir * uDrag * uWidth - n * uGather) * w;
    offset += vec2(dot(delta, radial), dot(delta, tangent));
  }
  fragColor = vec4(offset * uDecay, 0.0, 1.0);
}
`;

const starVertex = `
in vec4 aOrbit;
in vec4 aTraits;
uniform sampler2D uState;
uniform int uStride;
uniform vec2 uResolution;
uniform vec2 uCenter;
uniform float uFocal;
uniform float uDistance;
uniform float uIncline;
uniform float uRoll;
uniform float uPixelRatio;
uniform float uRealTime;
uniform float uDust;
uniform float uCore;
uniform float uVoid;
uniform float uSize;
uniform float uSizeScale;
uniform float uSparkle;
uniform float uTwinkle;
uniform float uBack;
uniform float uEnergy;
uniform float uInk;
uniform vec3 uLabA;
uniform vec3 uLabB;
uniform vec4 uRipples[${RIPPLES}];
uniform float uActive;
${orbitChunk}
out vec3 vColor;
out float vGain;

vec3 labToLinear(vec3 lab) {
  float l = lab.x + 0.3963377774 * lab.y + 0.2158037573 * lab.z;
  float m = lab.x - 0.1055613458 * lab.y - 0.0638541728 * lab.z;
  float s = lab.x - 0.0894841775 * lab.y - 1.2914855480 * lab.z;
  l = l * l * l;
  m = m * m * m;
  s = s * s * s;
  return max(vec3(
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.7076147010 * s
  ), 0.0);
}

void main() {
  float a = aOrbit.x;
  float bulge = aTraits.x;
  float phase;
  vec3 at = orbit(a, aOrbit.y, bulge, phase);
  vec2 radial = vec2(cos(at.z), sin(at.z));
  vec2 tangent = vec2(-radial.y, radial.x);
  vec2 offset = texelFetch(uState, ivec2(gl_VertexID % uStride, gl_VertexID / uStride), 0).xy;
  vec3 p = vec3(at.xy + radial * offset.x + tangent * offset.y, aOrbit.z);
  float tidal = smoothstep(0.004, 0.03, length(offset));
  float ring = 0.0;
  if (uActive > 0.5) {
    for (int k = 0; k < ${RIPPLES}; k++) {
      vec4 wave = uRipples[k];
      if (wave.w <= 0.0) continue;
      float age = uRealTime - wave.z;
      if (age < 0.0 || age > ${RING_LIFE}.0) continue;
      vec2 rel = p.xy - wave.xy;
      float rr = length(rel);
      float front = 0.05 + 0.32 * age;
      float width = 0.07 + 0.03 * age;
      float g = (rr - front) / width;
      float band = exp(-g * g);
      float envelope = wave.w * smoothstep(0.0, 0.7, age) * exp(-age / 2.2) * smoothstep(0.03, 0.2, front + length(wave.xy));
      p.xy -= rel / max(rr, 1e-4) * (rr - front) * 0.55 * envelope * band;
      p.z += 0.03 * envelope * band;
      ring += envelope * band;
    }
  }

  float ci = cos(uIncline);
  float si = sin(uIncline);
  vec3 q = vec3(p.x, p.y * ci + p.z * si, -p.y * si + p.z * ci);
  float cr = cos(uRoll);
  float sr = sin(uRoll);
  q.xy = vec2(cr * q.x - sr * q.y, sr * q.x + cr * q.y);
  float depth = uDistance - q.z;
  vec2 screen = uCenter + uFocal * q.xy / depth;

  float mk = uArms * uTwist;
  float crest = (cos(phase) - mk * sin(phase)) / sqrt(1.0 + mk * mk);
  float arm = pow(max(crest, 0.0), 2.0) * (1.0 - bulge);
  float laneCrest = (cos(phase - 0.55) - mk * sin(phase - 0.55)) / sqrt(1.0 + mk * mk);
  float lane = pow(max(laneCrest, 0.0), 8.0) * (1.0 - arm) * (1.0 - bulge) * smoothstep(0.1, 0.3, a);

  float reach = smoothstep(0.02, 0.55, a);
  vec3 tone = labToLinear(mix(uLabB, uLabA, reach));
  vec3 core = labToLinear(vec3(min(uLabB.x * 1.08 + 0.06, 0.98), uLabB.yz * 0.45));
  vec3 color = mix(tone, core, bulge * 0.75);
  vec3 youngColor = labToLinear(vec3(min(uLabB.x * 0.92, 0.9), uLabB.yz * 1.25));
  float young = smoothstep(0.84, 0.97, aTraits.z) * arm;
  float kindled = clamp(tidal * 0.7 + ring * 1.5, 0.0, 0.75) * (1.0 - bulge * 0.7);
  color = mix(color, youngColor, max(young * 0.75, kindled));
  float bright = step(1.0 - uSparkle, aTraits.y) * (1.0 - bulge * 0.6);
  color = mix(color, vec3(1.0, 0.97, 1.0), bright * 0.35);

  float twinkle = 1.0 - uTwinkle * 0.55 * (0.5 + 0.5 * sin(uRealTime * (0.6 + 1.6 * aTraits.w) + aTraits.w * 61.0));
  float body = mix(0.5 + 1.2 * arm + young * 1.4, uCore * 1.1, bulge) * (1.0 + bright * 2.2);
  body *= 1.0 - uDust * 0.8 * lane;
  float edge = 1.0 - smoothstep(0.82, 1.08, a);
  float hollow = smoothstep(uVoid * 0.85, uVoid * 1.15 + 0.01, a);
  float near = clamp(0.5 + 0.5 * q.z / 0.9, 0.0, 1.0);
  float gain = body * edge * hollow * twinkle * mix(uBack, 1.0, near) * (1.0 + uEnergy * 0.15);
  gain *= 1.0 + 0.9 * tidal * (1.0 - bulge * 0.7) + 0.5 * min(ring, 1.0);

  vColor = mix(color, labToLinear(vec3(uLabA.x * 0.62, uLabA.yz * 1.15)), uInk * 0.35);
  vGain = gain;
  float size = uSize * uSizeScale * uPixelRatio * mix(0.6, 1.1, fract(aTraits.y * 7.31)) * (1.0 + bright * 0.7 + young * 0.4);
  gl_PointSize = max(size * uDistance / depth, 1.0);
  gl_Position = vec4(screen / uResolution * 2.0 - 1.0, 0.0, 1.0);
}
`;

const starFragment = `
precision highp float;
uniform float uStarGain;
in vec3 vColor;
in float vGain;
out vec4 fragColor;

void main() {
  vec2 q = gl_PointCoord * 2.0 - 1.0;
  float r2 = dot(q, q);
  float e = (exp(-r2 * 6.0) + 0.25 * exp(-r2 * 2.0)) * vGain * uStarGain;
  fragColor = vec4(vColor * e, e);
}
`;

const passVertex = `
out vec2 vUv;

void main() {
  vUv = uv;
  gl_Position = vec4(position.xy, 0.0, 1.0);
}
`;

const downFragment = `
precision highp float;
uniform sampler2D uSource;
uniform vec2 uTexel;
in vec2 vUv;
out vec4 fragColor;

void main() {
  vec4 a = texture(uSource, vUv + uTexel * vec2(-1.0, -1.0));
  vec4 b = texture(uSource, vUv + uTexel * vec2(1.0, -1.0));
  vec4 c = texture(uSource, vUv + uTexel * vec2(-1.0, 1.0));
  vec4 d = texture(uSource, vUv + uTexel * vec2(1.0, 1.0));
  fragColor = (a + b + c + d) * 0.125 + texture(uSource, vUv) * 0.5;
}
`;

const upFragment = `
precision highp float;
uniform sampler2D uSource;
uniform sampler2D uBase;
uniform vec2 uTexel;
in vec2 vUv;
out vec4 fragColor;

void main() {
  vec4 sum = texture(uSource, vUv) * 4.0;
  sum += texture(uSource, vUv + uTexel * vec2(-1.0, 0.0)) * 2.0;
  sum += texture(uSource, vUv + uTexel * vec2(1.0, 0.0)) * 2.0;
  sum += texture(uSource, vUv + uTexel * vec2(0.0, -1.0)) * 2.0;
  sum += texture(uSource, vUv + uTexel * vec2(0.0, 1.0)) * 2.0;
  sum += texture(uSource, vUv + uTexel * vec2(-1.0, -1.0));
  sum += texture(uSource, vUv + uTexel * vec2(1.0, -1.0));
  sum += texture(uSource, vUv + uTexel * vec2(-1.0, 1.0));
  sum += texture(uSource, vUv + uTexel * vec2(1.0, 1.0));
  fragColor = texture(uBase, vUv) + sum / 16.0;
}
`;

const compositeFragment = `
precision highp float;
uniform sampler2D uLines;
uniform sampler2D uBloom;
uniform vec3 uBackground;
uniform float uGlow;
uniform float uExposure;
uniform float uMode;
uniform float uGrain;
in vec2 vUv;
out vec4 fragColor;

float hash(vec2 p) {
  vec3 p3 = fract(vec3(p.xyx) * 0.1031);
  p3 += dot(p3, p3.yzx + 33.33);
  return fract((p3.x + p3.y) * p3.z);
}

vec3 encode(vec3 c) {
  c = clamp(c, 0.0, 1.0);
  return mix(c * 12.92, 1.055 * pow(c, vec3(1.0 / 2.4)) - 0.055, step(vec3(0.0031308), c));
}

void main() {
  vec4 lines = texture(uLines, vUv);
  vec4 bloom = texture(uBloom, vUv);
  vec3 color;
  if (uMode < 0.5) {
    vec3 light = (lines.rgb + bloom.rgb * uGlow) * uExposure;
    vec3 exposed = 1.0 - exp(-light);
    float peak = max(exposed.r, max(exposed.g, exposed.b));
    exposed = mix(exposed, vec3(peak), smoothstep(0.75, 1.0, peak) * 0.35);
    color = uBackground + (1.0 - uBackground) * exposed;
  } else {
    float energy = lines.a + bloom.a * uGlow * 0.2;
    vec3 hue = (lines.rgb + bloom.rgb * uGlow * 0.2) / max(energy, 1e-4);
    float cover = clamp(1.0 - exp(-energy * uExposure * 1.4), 0.0, 0.94);
    color = mix(uBackground, clamp(hue * 0.8, 0.0, 1.0), cover);
  }
  float grain = hash(gl_FragCoord.xy + uGrain * 311.0) - 0.5;
  fragColor = vec4(encode(color) + grain * (1.2 / 255.0), 1.0);
}
`;

const seeded = (start: number) => {
  let state = start;
  return () => {
    state = (state * 16807) % 2147483647;
    return state / 2147483647;
  };
};

const buildStars = (count: number, coreSize: number, thickness: number) => {
  const random = seeded(23);
  const gauss = () => {
    const u = Math.max(random(), 1e-9);
    return Math.sqrt(-2 * Math.log(u)) * Math.cos(TAU * random());
  };
  const stride = 8;
  const rows = Math.ceil(count / STRIDE);
  const data = new Float32Array(count * stride);
  const fixed = new Float32Array(STRIDE * rows * 4);
  for (let i = 0; i < count; i++) {
    const bulge = random() < 0.16;
    let a: number;
    let z: number;
    if (bulge) {
      const u = Math.min(Math.max(random(), 1e-4), 0.995);
      a = Math.min(coreSize / Math.sqrt(Math.pow(u, -2 / 3) - 1), coreSize * 4);
      z = gauss() * a * 0.5;
    } else {
      do {
        a = -0.32 * Math.log(Math.max(random() * random(), 1e-9));
      } while (a > 1.08);
      a = Math.max(a, 0.012);
      z = gauss() * thickness * (0.5 + 0.5 * Math.exp(-a / 0.25));
    }
    const psi = random() * TAU;
    data.set([a, psi, z, random(), bulge ? 1 : 0, random(), random(), random()], i * stride);
    fixed.set([a, psi, bulge ? 1 : 0, 0], i * 4);
  }
  const geometry = new THREE.BufferGeometry();
  const buffer = new THREE.InterleavedBuffer(data, stride);
  geometry.setAttribute("aOrbit", new THREE.InterleavedBufferAttribute(buffer, 4, 0));
  geometry.setAttribute("aTraits", new THREE.InterleavedBufferAttribute(buffer, 4, 4));
  geometry.setAttribute("position", new THREE.InterleavedBufferAttribute(buffer, 3, 0));
  const texture = new THREE.DataTexture(fixed, STRIDE, rows, THREE.RGBAFormat, THREE.FloatType);
  texture.minFilter = THREE.NearestFilter;
  texture.magFilter = THREE.NearestFilter;
  texture.generateMipmaps = false;
  texture.needsUpdate = true;
  return { geometry, texture, rows };
};

const createGalaxy = (root: HTMLDivElement, settingsRef: { current: Settings }): Controller | null => {
  const canvas = document.createElement("canvas");
  canvas.setAttribute("aria-hidden", "true");
  canvas.style.position = "absolute";
  canvas.style.inset = "0";
  canvas.style.width = "100%";
  canvas.style.height = "100%";
  canvas.style.display = "block";
  canvas.style.pointerEvents = "none";
  root.prepend(canvas);

  let renderer: THREE.WebGLRenderer;
  try {
    renderer = new THREE.WebGLRenderer({ canvas, alpha: false, antialias: false, powerPreference: "high-performance" });
  } catch {
    canvas.remove();
    return null;
  }
  if (!renderer.capabilities.isWebGL2) {
    renderer.dispose();
    canvas.remove();
    return null;
  }
  renderer.autoClear = false;
  const texelType =
    renderer.extensions.has("EXT_color_buffer_float") || renderer.extensions.has("EXT_color_buffer_half_float")
      ? THREE.HalfFloatType
      : THREE.UnsignedByteType;

  const camera = new THREE.OrthographicCamera(-1, 1, 1, -1, 0, 1);
  const plane = new THREE.PlaneGeometry(2, 2);
  const ripples = Array.from({ length: RIPPLES }, () => new THREE.Vector4(0, 0, -100, 0));
  const stateType = renderer.extensions.has("EXT_color_buffer_float") ? THREE.FloatType : THREE.HalfFloatType;
  const orbitUniforms = {
    uTime: { value: 0 },
    uSpin: { value: 0.11 },
    uCoreRadius: { value: 0.12 },
    uArms: { value: 3 },
    uTwist: { value: 3.5 },
    uEps: { value: 0.1 },
    uPattern: { value: 0.04 },
  };

  const starMaterial = new THREE.ShaderMaterial({
    glslVersion: THREE.GLSL3,
    uniforms: {
      ...orbitUniforms,
      uState: { value: null },
      uStride: { value: STRIDE },
      uResolution: { value: new THREE.Vector2(1, 1) },
      uCenter: { value: new THREE.Vector2() },
      uFocal: { value: 1 },
      uDistance: { value: DISTANCE },
      uIncline: { value: 1 },
      uRoll: { value: 0 },
      uPixelRatio: { value: 1 },
      uRealTime: { value: 0 },
      uDust: { value: 0.5 },
      uCore: { value: 1 },
      uVoid: { value: 0 },
      uSize: { value: 1.4 },
      uSizeScale: { value: 1 },
      uSparkle: { value: 0.08 },
      uTwinkle: { value: 0.4 },
      uBack: { value: 0.5 },
      uEnergy: { value: 0 },
      uInk: { value: 0 },
      uLabA: { value: new THREE.Vector3() },
      uLabB: { value: new THREE.Vector3() },
      uRipples: { value: ripples },
      uActive: { value: 0 },
      uStarGain: { value: 0.3 },
    },
    vertexShader: starVertex,
    fragmentShader: starFragment,
    depthTest: false,
    depthWrite: false,
    transparent: true,
    blending: THREE.CustomBlending,
    blendEquation: THREE.AddEquation,
    blendSrc: THREE.OneFactor,
    blendDst: THREE.OneFactor,
    blendSrcAlpha: THREE.OneFactor,
    blendDstAlpha: THREE.OneFactor,
  });

  const pass = (fragmentShader: string, uniforms: Record<string, THREE.IUniform>) =>
    new THREE.ShaderMaterial({
      glslVersion: THREE.GLSL3,
      uniforms,
      vertexShader: passVertex,
      fragmentShader,
      depthTest: false,
      depthWrite: false,
      blending: THREE.NoBlending,
    });

  const stateMaterial = pass(stateFragment, {
    ...orbitUniforms,
    uStatic: { value: null },
    uState: { value: null },
    uSegment: { value: new THREE.Vector4() },
    uGather: { value: 0 },
    uDrag: { value: 0 },
    uWidth: { value: WAKE_WIDTH },
    uDecay: { value: 1 },
  });
  const downMaterial = pass(downFragment, { uSource: { value: null }, uTexel: { value: new THREE.Vector2() } });
  const upMaterial = pass(upFragment, {
    uSource: { value: null },
    uBase: { value: null },
    uTexel: { value: new THREE.Vector2() },
  });
  const compositeMaterial = pass(compositeFragment, {
    uLines: { value: null },
    uBloom: { value: null },
    uBackground: { value: new THREE.Vector3() },
    uGlow: { value: 0.5 },
    uExposure: { value: 1 },
    uMode: { value: 0 },
    uGrain: { value: 0 },
  });

  const starPoints = new THREE.Points(new THREE.BufferGeometry(), starMaterial);
  starPoints.frustumCulled = false;
  const starScene = new THREE.Scene();
  starScene.add(starPoints);
  const quad = new THREE.Mesh(plane, compositeMaterial);
  quad.frustumCulled = false;
  const quadScene = new THREE.Scene();
  quadScene.add(quad);
  let starKey = "";
  let staticTexture: THREE.DataTexture | null = null;
  let stateTargets: THREE.WebGLRenderTarget[] = [];
  let stateIndex = 0;

  const target = (w: number, h: number) =>
    new THREE.WebGLRenderTarget(Math.max(1, w), Math.max(1, h), {
      type: texelType,
      format: THREE.RGBAFormat,
      minFilter: THREE.LinearFilter,
      magFilter: THREE.LinearFilter,
      depthBuffer: false,
      stencilBuffer: false,
      generateMipmaps: false,
    });

  let lineTarget: THREE.WebGLRenderTarget | null = null;
  let downTargets: THREE.WebGLRenderTarget[] = [];
  let upTargets: THREE.WebGLRenderTarget[] = [];

  let destroyed = false;
  let raf = 0;
  let last = 0;
  let visible = true;
  let clock = 40;
  let realClock = 0;
  let energy = 0;
  let width = 1;
  let height = 1;
  let pixelRatio = 1;
  let grain = 0;
  let lastTouch = -100;
  const pointer = {
    inside: false,
    disc: null as { x: number; y: number } | null,
    trail: null as { x: number; y: number } | null,
  };

  const release = () => {
    lineTarget?.dispose();
    downTargets.forEach((item) => item.dispose());
    upTargets.forEach((item) => item.dispose());
    lineTarget = null;
    downTargets = [];
    upTargets = [];
  };

  const draw = (material: THREE.ShaderMaterial, output: THREE.WebGLRenderTarget | null) => {
    quad.material = material;
    renderer.setRenderTarget(output);
    renderer.render(quadScene, camera);
  };

  const clearState = () => {
    for (const item of stateTargets) {
      renderer.setRenderTarget(item);
      renderer.setClearColor(0x000000, 0);
      renderer.clear();
    }
  };

  const ensureStars = () => {
    const settings = settingsRef.current;
    const count = Math.round(clamp(settings.stars, 2000, 250000));
    const coreSize = clamp(settings.coreSize, 0.02, 0.3);
    const thickness = clamp(settings.thickness, 0, 0.2);
    const key = [count, coreSize, thickness].join("|");
    if (key === starKey) return;
    starKey = key;
    starPoints.geometry.dispose();
    staticTexture?.dispose();
    stateTargets.forEach((item) => item.dispose());
    const built = buildStars(count, coreSize, thickness);
    starPoints.geometry = built.geometry;
    staticTexture = built.texture;
    stateTargets = [0, 1].map(
      () =>
        new THREE.WebGLRenderTarget(STRIDE, built.rows, {
          type: stateType,
          format: THREE.RGBAFormat,
          minFilter: THREE.NearestFilter,
          magFilter: THREE.NearestFilter,
          depthBuffer: false,
          stencilBuffer: false,
          generateMipmaps: false,
        }),
    );
    stateIndex = 0;
    clearState();
    stateMaterial.uniforms.uStatic.value = staticTexture;
    starMaterial.uniforms.uState.value = stateTargets[0].texture;
  };

  const view = () => {
    const settings = settingsRef.current;
    const w = width * pixelRatio;
    const h = height * pixelRatio;
    const span = clamp(settings.scale, 0.2, 4) * 0.36 * Math.min(w, h * 1.7);
    return {
      incline: (clamp(settings.tilt, 0, 85) * Math.PI) / 180,
      roll: (clamp(settings.roll, -180, 180) * Math.PI) / 180,
      focal: span * DISTANCE,
      center: [clamp(settings.centerX, -0.5, 1.5) * w, (1 - clamp(settings.centerY, -0.5, 1.5)) * h] as const,
    };
  };

  const toDisc = (clientX: number, clientY: number) => {
    const rect = root.getBoundingClientRect();
    const camera2 = view();
    const px = (clientX - rect.left) * pixelRatio;
    const py = (rect.height - (clientY - rect.top)) * pixelRatio;
    const ux = (px - camera2.center[0]) / camera2.focal;
    const uy = (py - camera2.center[1]) / camera2.focal;
    const cr = Math.cos(camera2.roll);
    const sr = Math.sin(camera2.roll);
    const rx = cr * ux + sr * uy;
    const ry = -sr * ux + cr * uy;
    const ci = Math.cos(camera2.incline);
    const si = Math.sin(camera2.incline);
    const denominator = ci - ry * si;
    if (denominator <= 0.02) return null;
    const y = (ry * DISTANCE) / denominator;
    const x = rx * (DISTANCE + y * si);
    return { x, y };
  };

  const ripple = (x = 0, y = 0) => {
    const settings = settingsRef.current;
    if (settings.reduced || settings.paused) return;
    const strength = clamp(settings.rippleStrength, 0, 3);
    let newest = 0;
    let oldest = 0;
    for (let k = 1; k < RIPPLES; k++) {
      if (ripples[k].z > ripples[newest].z) newest = k;
      if (ripples[k].z < ripples[oldest].z) oldest = k;
    }
    const recent = ripples[newest];
    if (recent.w > 0 && realClock - recent.z < 0.35 && Math.hypot(recent.x - x, recent.y - y) < 0.15) {
      recent.w = Math.min(recent.w + strength * 0.3, strength * 1.6);
    } else if (realClock - ripples[oldest].z > RING_LIFE) {
      ripples[oldest].set(x, y, realClock, strength);
    } else {
      recent.w = Math.min(recent.w + strength * 0.25, strength * 1.6);
    }
    wake();
  };

  const disturbed = () => ripples.some((item) => item.w > 0 && realClock - item.z < RING_LIFE);

  const advance = (dt: number) => {
    const settings = settingsRef.current;
    if (!stateTargets.length || settings.paused || settings.reduced || dt <= 0) return;
    let gather = 0;
    let drag = 0;
    const from = pointer.trail;
    const to = pointer.disc;
    const uniforms = stateMaterial.uniforms;
    if (pointer.inside && settings.interactive && settings.hoverWake && from && to) {
      const length = Math.hypot(to.x - from.x, to.y - from.y);
      if (length > 1e-5) {
        const pace = length / Math.max(dt, 1 / 240);
        const t = clamp((pace - 0.05) / 1.15, 0, 1);
        const effect = 0.9 * clamp(settings.wakeStrength, 0, 3) * t * t * (3 - 2 * t);
        gather = (effect * length) / (length + WAKE_WIDTH * 1.772);
        drag = gather * 0.25;
        uniforms.uSegment.value.set(from.x, from.y, to.x, to.y);
        if (gather > 0) lastTouch = realClock;
      }
    }
    pointer.trail = to ? { x: to.x, y: to.y } : null;
    if (gather <= 0 && realClock - lastTouch > RELAX * 7) return;
    uniforms.uGather.value = gather;
    uniforms.uDrag.value = drag;
    uniforms.uDecay.value = Math.exp(-dt / RELAX);
    uniforms.uState.value = stateTargets[stateIndex].texture;
    uniforms.uTime.value = clock;
    quad.material = stateMaterial;
    renderer.setRenderTarget(stateTargets[1 - stateIndex]);
    renderer.render(quadScene, camera);
    stateIndex = 1 - stateIndex;
    starMaterial.uniforms.uState.value = stateTargets[stateIndex].texture;
  };

  const apply = () => {
    const settings = settingsRef.current;
    const uniforms = starMaterial.uniforms;
    const w = width * pixelRatio;
    const h = height * pixelRatio;
    const lens = view();
    const background = parseColor(settings.backgroundColor, [0.04, 0.04, 0.04]);
    const luminance = 0.2126 * background[0] + 0.7152 * background[1] + 0.0722 * background[2];
    const ink = settings.mode === "ink" || (settings.mode === "auto" && luminance > 0.5);
    const labA = toOklab(parseColor(settings.colors[0], [0.48, 0.3, 1]));
    const labB = toOklab(parseColor(settings.colors[1], [1, 0.78, 0.95]));
    const arms = Math.round(clamp(settings.arms, 1, 8));
    const twist = clamp(settings.twist, 0.5, 8);
    const contrast = clamp(settings.armStrength, 0, 1);
    uniforms.uResolution.value.set(w, h);
    uniforms.uCenter.value.set(lens.center[0], lens.center[1]);
    uniforms.uFocal.value = lens.focal;
    uniforms.uIncline.value = lens.incline;
    uniforms.uRoll.value = lens.roll;
    uniforms.uPixelRatio.value = pixelRatio;
    orbitUniforms.uTime.value = clock;
    uniforms.uRealTime.value = realClock;
    orbitUniforms.uArms.value = arms;
    orbitUniforms.uTwist.value = twist;
    orbitUniforms.uEps.value = (0.92 * contrast) / Math.sqrt(1 + arms * arms * twist * twist);
    uniforms.uDust.value = clamp(settings.dust, 0, 1);
    uniforms.uCore.value = clamp(settings.core, 0, 3);
    uniforms.uVoid.value = clamp(settings.innerVoid, 0, 0.6);
    uniforms.uSize.value = clamp(settings.starSize, 0.3, 6);
    uniforms.uSparkle.value = clamp(settings.sparkle, 0, 1) * 0.2;
    uniforms.uTwinkle.value = settings.reduced ? 0 : clamp(settings.twinkle, 0, 1);
    uniforms.uBack.value = 1 - clamp(settings.depth, 0, 1);
    uniforms.uEnergy.value = energy;
    uniforms.uInk.value = ink ? 1 : 0;
    uniforms.uLabA.value.set(labA[0], labA[1], labA[2]);
    uniforms.uLabB.value.set(labB[0], labB[1], labB[2]);
    uniforms.uActive.value = disturbed() ? 1 : 0;
    const count = Math.round(clamp(settings.stars, 2000, 250000));
    const area = Math.pow(lens.focal / DISTANCE / pixelRatio / 380, 2);
    const share = clamp(area, 0.2, 1);
    const sizeScale = clamp(Math.pow(area, 0.25), 0.9, 1.22);
    starPoints.geometry.setDrawRange(0, Math.round(count * share));
    uniforms.uSizeScale.value = sizeScale;
    uniforms.uStarGain.value =
      Math.sqrt(60000 / count) * 0.3 * clamp(area / (share * sizeScale * sizeScale), 0.5, 1.5);
    const composite = compositeMaterial.uniforms;
    composite.uBackground.value.set(toLinear(background[0]), toLinear(background[1]), toLinear(background[2]));
    composite.uGlow.value = clamp(settings.glow, 0, 3);
    composite.uExposure.value = (ink ? 4.5 : 1) * clamp(settings.intensity, 0, 4) * (1 + energy * 0.08);
    composite.uMode.value = ink ? 1 : 0;
    composite.uGrain.value = grain;
  };

  const render = () => {
    if (!lineTarget || !downTargets.length) return;
    ensureStars();
    apply();
    renderer.setRenderTarget(lineTarget);
    renderer.setClearColor(0x000000, 0);
    renderer.clear();
    renderer.render(starScene, camera);
    let source = lineTarget;
    for (const item of downTargets) {
      downMaterial.uniforms.uSource.value = source.texture;
      downMaterial.uniforms.uTexel.value.set(1 / source.width, 1 / source.height);
      draw(downMaterial, item);
      source = item;
    }
    let accumulated = downTargets[downTargets.length - 1];
    for (let k = downTargets.length - 2; k >= 0; k--) {
      upMaterial.uniforms.uSource.value = accumulated.texture;
      upMaterial.uniforms.uBase.value = downTargets[k].texture;
      upMaterial.uniforms.uTexel.value.set(1 / accumulated.width, 1 / accumulated.height);
      draw(upMaterial, upTargets[k]);
      accumulated = upTargets[k];
    }
    compositeMaterial.uniforms.uLines.value = lineTarget.texture;
    compositeMaterial.uniforms.uBloom.value = accumulated.texture;
    draw(compositeMaterial, null);
  };

  const hoverGoal = () => {
    const settings = settingsRef.current;
    return pointer.inside && settings.interactive && !settings.reduced ? clamp(settings.hoverBoost, 0, 2) : 0;
  };

  const step = (dt: number) => {
    const settings = settingsRef.current;
    energy += (hoverGoal() - energy) * (1 - Math.exp(-dt / 0.5));
    if (settings.paused || settings.reduced) return;
    realClock += dt;
    clock += dt * clamp(settings.speed, -4, 4) * (1 + energy * 0.25);
    grain = (grain + 0.618034) % 1;
  };

  const tick = (now: number) => {
    raf = 0;
    if (destroyed) return;
    const dt = Math.min(0.05, Math.max(0, (now - last) / 1000));
    last = now;
    step(dt);
    ensureStars();
    advance(dt);
    render();
    const settings = settingsRef.current;
    const animating = !settings.paused && !settings.reduced;
    const settling = Math.abs(energy - hoverGoal()) > 0.002 || disturbed() || realClock - lastTouch < RELAX * 7;
    if (visible && !document.hidden && (animating || settling)) raf = requestAnimationFrame(tick);
  };

  function wake() {
    if (destroyed || raf || !visible) return;
    last = performance.now();
    raf = requestAnimationFrame(tick);
  }

  const resize = () => {
    const rect = root.getBoundingClientRect();
    width = Math.max(1, rect.width);
    height = Math.max(1, rect.height);
    pixelRatio = Math.min(window.devicePixelRatio || 1, 2) * clamp(settingsRef.current.quality, 0.25, 1);
    renderer.setPixelRatio(pixelRatio);
    renderer.setSize(width, height, false);
    const w = Math.round(width * pixelRatio);
    const h = Math.round(height * pixelRatio);
    release();
    lineTarget = target(w, h);
    const levels = clamp(Math.floor(Math.log2(Math.min(w, h) / 8)), 3, 7);
    for (let k = 1; k <= levels; k++) {
      downTargets.push(target(Math.max(1, w >> k), Math.max(1, h >> k)));
      upTargets.push(target(Math.max(1, w >> k), Math.max(1, h >> k)));
    }
    render();
    wake();
  };

  const onMove = (event: PointerEvent) => {
    const settings = settingsRef.current;
    if (event.pointerType === "touch" || !settings.interactive) return;
    if (!pointer.inside) {
      pointer.inside = true;
      pointer.trail = null;
      wake();
    }
    const point = toDisc(event.clientX, event.clientY);
    pointer.disc = point && Math.hypot(point.x, point.y) < 1.6 ? point : null;
    if (!pointer.disc) pointer.trail = null;
    wake();
  };

  const onLeave = () => {
    pointer.inside = false;
    pointer.disc = null;
    pointer.trail = null;
    wake();
  };

  const onDown = (event: PointerEvent) => {
    const settings = settingsRef.current;
    if (event.button !== 0 || !settings.interactive || !settings.clickRipple) return;
    const point = toDisc(event.clientX, event.clientY);
    const inside = point && Math.hypot(point.x, point.y) < 1.2;
    ripple(inside ? point.x : 0, inside ? point.y : 0);
  };

  root.addEventListener("pointermove", onMove);
  root.addEventListener("pointerleave", onLeave);
  root.addEventListener("pointercancel", onLeave);
  root.addEventListener("pointerdown", onDown);
  const resizeObserver = new ResizeObserver(resize);
  resizeObserver.observe(root);
  const intersection = new IntersectionObserver(
    (entries) => {
      visible = entries.some((entry) => entry.isIntersecting);
      if (visible) wake();
    },
    { rootMargin: "80px" },
  );
  intersection.observe(root);
  const onVisibility = () => {
    if (!document.hidden) wake();
  };
  document.addEventListener("visibilitychange", onVisibility);
  const onLost = (event: Event) => event.preventDefault();
  canvas.addEventListener("webglcontextlost", onLost);
  resize();

  return {
    sync: () => {
      const ratio = Math.min(window.devicePixelRatio || 1, 2) * clamp(settingsRef.current.quality, 0.25, 1);
      if (Math.abs(ratio - pixelRatio) > 1e-3) resize();
      else render();
      wake();
    },
    destroy: () => {
      destroyed = true;
      cancelAnimationFrame(raf);
      root.removeEventListener("pointermove", onMove);
      root.removeEventListener("pointerleave", onLeave);
      root.removeEventListener("pointercancel", onLeave);
      root.removeEventListener("pointerdown", onDown);
      document.removeEventListener("visibilitychange", onVisibility);
      canvas.removeEventListener("webglcontextlost", onLost);
      resizeObserver.disconnect();
      intersection.disconnect();
      release();
      starPoints.geometry.dispose();
      staticTexture?.dispose();
      stateTargets.forEach((item) => item.dispose());
      plane.dispose();
      [starMaterial, stateMaterial, downMaterial, upMaterial, compositeMaterial].forEach((item) => item.dispose());
      renderer.dispose();
      renderer.forceContextLoss();
      canvas.remove();
    },
    ripple: () => ripple(0, 0),
  };
};

const BinaryOrbits = forwardRef<BinaryOrbitsHandle, BinaryOrbitsProps>(function BinaryOrbits(
  {
    colors = ["#7B4DFF", "#FFC2EE"],
    backgroundColor = "#0A0A0A",
    mode = "auto",
    stars = 70000,
    starSize = 1.7,
    sparkle = 0.5,
    arms = 3,
    twist = 3.5,
    armStrength = 0.5,
    dust = 0.5,
    core = 1,
    coreSize = 0.07,
    innerVoid = 0,
    thickness = 0.02,
    tilt = 62,
    roll = -8,
    scale = 1,
    centerX = 0.5,
    centerY = 0.52,
    speed = 1,
    twinkle = 0.35,
    depth = 0.4,
    glow = 0.45,
    intensity = 1,
    interactive = true,
    hoverWake = true,
    clickRipple = true,
    wakeStrength = 1,
    rippleStrength = 1,
    hoverBoost = 0.5,
    paused = false,
    quality = 1,
    className,
    style,
    children,
  },
  ref,
) {
  const rootRef = useRef<HTMLDivElement>(null);
  const controllerRef = useRef<Controller | null>(null);
  const reduced = useSyncExternalStore(subscribeToMotion, readMotion, () => false);
  const settingsRef = useRef<Settings>({
    colors,
    backgroundColor,
    mode,
    stars,
    starSize,
    sparkle,
    arms,
    twist,
    armStrength,
    dust,
    core,
    coreSize,
    innerVoid,
    thickness,
    tilt,
    roll,
    scale,
    centerX,
    centerY,
    speed,
    twinkle,
    depth,
    glow,
    intensity,
    interactive,
    hoverWake,
    clickRipple,
    wakeStrength,
    rippleStrength,
    hoverBoost,
    paused,
    quality,
    reduced,
  });

  useEffect(() => {
    settingsRef.current = {
      colors,
      backgroundColor,
      mode,
      stars,
      starSize,
      sparkle,
      arms,
      twist,
      armStrength,
      dust,
      core,
      coreSize,
      innerVoid,
      thickness,
      tilt,
      roll,
      scale,
      centerX,
      centerY,
      speed,
      twinkle,
      depth,
      glow,
      intensity,
      interactive,
      hoverWake,
      clickRipple,
      wakeStrength,
      rippleStrength,
      hoverBoost,
      paused,
      quality,
      reduced,
    };
    controllerRef.current?.sync();
  });

  useEffect(() => {
    const root = rootRef.current;
    if (!root) return;
    const controller = createGalaxy(root, settingsRef);
    controllerRef.current = controller;
    return () => {
      controller?.destroy();
      controllerRef.current = null;
    };
  }, []);

  useImperativeHandle(
    ref,
    () => ({
      ripple: () => controllerRef.current?.ripple(),
    }),
    [],
  );

  return (
    <div
      ref={rootRef}
      className={["binary-orbits", className].filter(Boolean).join(" ")}
      style={{ backgroundColor, ...style }}
    >
      {children && <div className="binary-orbits__content">{children}</div>}
    </div>
  );
});

BinaryOrbits.displayName = "BinaryOrbits";

export { BinaryOrbits };
export default BinaryOrbits;
