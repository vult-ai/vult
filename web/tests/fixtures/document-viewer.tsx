import { createRoot } from "react-dom/client";
import { DocumentViewer } from "../../src/components/book/document-viewer";
import "../../src/style.css";

const parameters = new URLSearchParams(location.search);
document.documentElement.classList.add(parameters.get("theme") === "dark" ? "dark-mode" : "light-mode");
const image = parameters.get("kind") === "image";
const presentation = parameters.get("presentation") === "recall" ? "recall" : "preview";

createRoot(document.getElementById("root")!).render(
  <DocumentViewer
    src={image ? "/tests/fixtures/document-viewer-image.png" : "/tests/fixtures/synthetic-hosting.pdf"}
    name={image ? "Example image" : "Example statement"}
    kind={image ? "image" : "pdf"}
    presentation={presentation}
  />,
);
