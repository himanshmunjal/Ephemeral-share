import { Routes, Route } from "react-router-dom";
import Header from "./components/Header.jsx";
import Footer from "./components/Footer.jsx";
import CreateSessionPage from "./pages/CreateSessionPage.jsx";
import JoinSessionPage from "./pages/JoinSessionPage.jsx";
import SessionRoomPage from "./pages/SessionRoomPage.jsx";

export default function App() {
  return (
    <div className="flex min-h-screen flex-col bg-slate-50 text-slate-900 dark:bg-slate-950 dark:text-slate-100">
      <Header />

      <main className="mx-auto w-full max-w-5xl flex-1 px-4 py-10">
        <Routes>
          <Route path="/" element={<CreateSessionPage />} />
          <Route path="/join" element={<JoinSessionPage />} />
          <Route path="/session/:code" element={<SessionRoomPage />} />
        </Routes>
      </main>

      <Footer />
    </div>
  );
}
