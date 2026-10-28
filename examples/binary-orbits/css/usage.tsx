import { useRef } from "react";
import BinaryOrbits, { type BinaryOrbitsHandle } from "./binary-orbits";

<div style={{ height: 600 }}>
  <BinaryOrbits />
</div>

<BinaryOrbits colors={["#6C8CFF", "#FFE0C7"]} arms={2} twist={2.6} tilt={74} roll={-18}>
  <h1 className="text-white">A universe of possibility.</h1>
</BinaryOrbits>

<BinaryOrbits backgroundColor="#FFFFFF" colors={["#5B2BFF", "#D13DB8"]} arms={5} innerVoid={0.14} />

const galaxy = useRef<BinaryOrbitsHandle>(null);

<BinaryOrbits ref={galaxy} />

galaxy.current?.ripple();