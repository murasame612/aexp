import { useEffect, type ReactNode } from "react";
import { motion } from "framer-motion";
import { X } from "lucide-react";

let openModalCount = 0;

export function Modal({ title, onClose, children, wide }: { title: string; onClose: () => void; children: ReactNode; wide?: boolean }) {
  useEffect(() => {
    openModalCount += 1;
    document.documentElement.classList.add("modal-open");
    document.body.classList.add("modal-open");
    return () => {
      openModalCount = Math.max(0, openModalCount - 1);
      if (openModalCount === 0) {
        document.documentElement.classList.remove("modal-open");
        document.body.classList.remove("modal-open");
      }
    };
  }, []);
  return (
    <div className="modal-backdrop" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <motion.div className={wide ? "modal wide" : "modal"} initial={{ opacity: 0, scale: 0.98, y: 12 }} animate={{ opacity: 1, scale: 1, y: 0 }} exit={{ opacity: 0, scale: 0.98, y: 12 }}>
        <header><h2>{title}</h2><button className="icon-button" onClick={onClose}><X size={18} /></button></header>
        {children}
      </motion.div>
    </div>
  );
}
