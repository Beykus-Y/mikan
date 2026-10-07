import clsx from "clsx";
import logo from "../assets/logo.webp";

/**
 * The backdrop behind the glass, "dawn": warm light from the left — mandarin, rose,
 * honey — and cool from the right — lilac, sky. The two drift apart slowly; paper grain
 * on top. `calm` keeps it still (the subscription page, see .atmo.calm in app.css).
 */
export function Atmosphere({ calm }: { calm?: boolean }) {
  return (
    <div className={clsx("atmo", calm && "calm")} aria-hidden>
      <span className="glow glow-warm" />
      <span className="glow glow-cool" />
      <span className="grain" />
    </div>
  );
}

/** The painted mandarin on a transparent ground: it reads the same in every theme. */
export function Logo({ size = 32 }: { size?: number }) {
  return <img src={logo} width={size} height={size} alt="" aria-hidden decoding="async" draggable={false} className="shrink-0" />;
}
