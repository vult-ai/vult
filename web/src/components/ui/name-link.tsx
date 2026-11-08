import type { ComponentPropsWithRef, ReactNode } from "react";
import { cn } from "../../lib/utils";
import "./name-link.css";

type Content = {
  name: string;
  leading?: ReactNode;
  trailing?: ReactNode;
  nameClassName?: string;
};
type ButtonProps = Content &
  Omit<ComponentPropsWithRef<"button">, "children"> & { href?: never };
type AnchorProps = Content &
  Omit<ComponentPropsWithRef<"a">, "children"> & { href: string };

/** Only the name is decorated. Avatars, icons and metadata have separate slots. */
export function NameLink(props: ButtonProps | AnchorProps) {
  const { name, leading, trailing, nameClassName, className, ...native } =
    props;
  const content = (
    <>
      {leading != null && leading !== false && (
        <span className="vult-name-link-decoration">{leading}</span>
      )}
      <span className={cn("vult-name-link-label", nameClassName)}>{name}</span>
      {trailing != null && trailing !== false && (
        <span className="vult-name-link-decoration">{trailing}</span>
      )}
    </>
  );
  const classes = cn(
    "vult-name-link vult-focus-target vult-focus-contained",
    className,
  );
  if ("href" in native && native.href !== undefined) {
    const anchor = native as Omit<AnchorProps, keyof Content>;
    return (
      <a
        {...anchor}
        className={classes}
        onPointerDown={(event) => {
          event.stopPropagation();
          anchor.onPointerDown?.(event);
        }}
        onMouseDown={(event) => {
          event.stopPropagation();
          anchor.onMouseDown?.(event);
        }}
        onClick={(event) => {
          event.stopPropagation();
          anchor.onClick?.(event);
        }}
      >
        {content}
      </a>
    );
  }
  const button = native as Omit<ButtonProps, keyof Content>;
  return (
    <button
      {...button}
      type={button.type || "button"}
      className={classes}
      onPointerDown={(event) => {
        event.stopPropagation();
        button.onPointerDown?.(event);
      }}
      onMouseDown={(event) => {
        event.stopPropagation();
        button.onMouseDown?.(event);
      }}
      onClick={(event) => {
        event.stopPropagation();
        button.onClick?.(event);
      }}
    >
      {content}
    </button>
  );
}
