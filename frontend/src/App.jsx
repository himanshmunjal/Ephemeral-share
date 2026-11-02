import { Routes, Route } from "react-router-dom";
import Header from "./components/Header.jsx";
import Footer from "./components/Footer.jsx";
import FreeTierNotice from "./components/FreeTierNotice.jsx";
import CreateSessionPage from "./pages/CreateSessionPage.jsx";
import JoinSessionPage from "./pages/JoinSessionPage.jsx";
import SessionRoomPage from "./pages/SessionRoomPage.jsx";
import { PrivacyPage, TermsPage, AbusePage } from "./pages/LegalPages.jsx";
import SessionGone from "./components/SessionGone.jsx";

export default function App() {
  return (
    <div className="flex min-h-screen flex-col text-slate-900 dark:text-slate-100">
      <Header />
      <FreeTierNotice />

      <main className="mx-auto w-full max-w-5xl flex-1 px-4 py-10">
        <Routes>
          <Route path="/" element={<CreateSessionPage />} />
          <Route path="/join" element={<JoinSessionPage />} />
          <Route path="/session/:code" element={<SessionRoomPage />} />
          <Route path="/privacy" element={<PrivacyPage />} />
          <Route path="/terms" element={<TermsPage />} />
          <Route path="/abuse" element={<AbusePage />} />
          <Route path="*" element={<SessionGone kind="not_found" />} />
        </Routes>
      </main>

      <Footer />
    </div>
  );
}
