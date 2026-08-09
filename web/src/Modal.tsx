import { useEffect, useId, useRef, type ReactNode } from "react";
import { motion } from "framer-motion";
import { X } from "lucide-react";

let openModalCount = 0;
const modalStack: string[] = [];

export function Modal({ title, onClose, children, wide }: { title: string; onClose: () => void; children: ReactNode; wide?: boolean }) {
  const dialogID = useId();
  const titleID = `${dialogID}-title`;
  const closeButtonRef = useRef<HTMLButtonElement | null>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  useEffect(() => {
    const previouslyFocused = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    openModalCount += 1;
    modalStack.push(dialogID);
    document.documentElement.classList.add("modal-open");
    document.body.classList.add("modal-open");
    const focusTimer = window.setTimeout(() => closeButtonRef.current?.focus(), 0);
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || modalStack[modalStack.length - 1] !== dialogID) return;
      event.preventDefault();
      onCloseRef.current();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.clearTimeout(focusTimer);
      window.removeEventListener("keydown", onKeyDown);
      const stackIndex = modalStack.lastIndexOf(dialogID);
      if (stackIndex >= 0) modalStack.splice(stackIndex, 1);
      openModalCount = Math.max(0, openModalCount - 1);
      if (openModalCount === 0) {
        document.documentElement.classList.remove("modal-open");
        document.body.classList.remove("modal-open");
      }
      previouslyFocused?.focus();
    };
  }, [dialogID]);
  return (
    <div className="modal-backdrop" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <motion.div className={wide ? "modal wide" : "modal"} role="dialog" aria-modal="true" aria-labelledby={titleID} initial={{ opacity: 0, scale: 0.98, y: 12 }} animate={{ opacity: 1, scale: 1, y: 0 }} exit={{ opacity: 0, scale: 0.98, y: 12 }}>
        <header><h2 id={titleID}>{title}</h2><button ref={closeButtonRef} className="icon-button" aria-label="Close" onClick={onClose}><X size={18} /></button></header>
        {children}
      </motion.div>
    </div>
  );
}
