import { Link } from "react-router-dom";

// Set VITE_CONTACT_EMAIL at build time to the mailbox that receives abuse
// reports and takedown requests.
const CONTACT = import.meta.env.VITE_CONTACT_EMAIL || "munjalhimansh2211@gmail.com";

// NOTE FOR THE OPERATOR: these pages describe how the software actually
// behaves, in plain language. They are a starting point, not legal advice —
// have them reviewed for your jurisdiction before launching publicly.

function Page({ title, updated, children }) {
  return (
    <article className="mx-auto max-w-2xl">
      <h1 className="text-2xl font-semibold text-slate-900 dark:text-slate-50">{title}</h1>
      <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">Last updated {updated}</p>
      <div className="mt-6 space-y-5 text-sm leading-relaxed text-slate-600 dark:text-slate-300">{children}</div>
      <p className="mt-10 text-sm">
        <Link to="/" className="text-brand-600 hover:underline dark:text-brand-500">
          ← Back to ephemeral.share
        </Link>
      </p>
    </article>
  );
}

const H = ({ children }) => <h2 className="pt-2 text-base font-semibold text-slate-800 dark:text-slate-100">{children}</h2>;

export function PrivacyPage() {
  return (
    <Page title="Privacy" updated="November 2026">
      <p>ephemeral.share is designed so that what you share is stored encrypted and we have no reason to look at it. This page explains exactly what that does and doesn’t guarantee.</p>

      <H>What we don’t collect</H>
      <p>No accounts, no emails, no names, no tracking cookies, no advertising or third-party analytics.</p>

      <H>What you share</H>
      <p>
        Files, notes, file names and display names are encrypted in your browser before they leave your device, and are stored on our
        servers only as ciphertext. The encryption key is derived from the session’s access code. Anyone who has the access code can
        decrypt the session’s content — treat the code like a password.
      </p>

      <H>What this does and doesn’t protect against</H>
      <p>
        Encryption protects your content from anyone who only gets at stored data: a leaked storage bucket, a database dump, or a storage
        provider. It is <strong>not</strong> a guarantee against us as the operator: the access code is also how a session is looked up, so
        our server receives it with each request and could in principle use it to decrypt. We don’t store the code, don’t write it to our
        logs, and don’t look at, scan or analyse content — but the system is built on that being our practice, not on it being technically
        impossible. If you need protection from the operator, encrypt the file yourself before sharing it.
      </p>

      <H>How long it’s kept</H>
      <p>
        Every session has a timer. When it ends — or when the host ends it early — the session and all of its files are deleted. Items
        deleted by their uploader or the host are removed immediately. A background job also removes any stray files left behind by an
        interrupted session.
      </p>

      <H>Technical data</H>
      <ul className="list-disc space-y-1 pl-5">
        <li>
          <strong>IP address:</strong> used briefly (in memory and short-lived counters) for rate limiting and to block repeated wrong-code
          guesses. Counters expire automatically within minutes.
        </li>
        <li>
          <strong>Random browser ID:</strong> a random value stored in your browser so you can delete your own uploads. It is not linked to a
          person and is never shown to other participants.
        </li>
        <li>
          <strong>Display name:</strong> a random name like “Swift Otter”, which you can change. It is stored in your browser and sent to the
          session only in encrypted form. It isn’t linked to you.
        </li>
        <li>
          <strong>Server logs and error reports:</strong> record the kind of request, status and timing, and error details for reliability and
          abuse prevention. Access codes are removed from them before they are stored or sent to our error-tracking service.
        </li>
        <li>
          <strong>Aggregate statistics (if enabled):</strong> counts such as how many sessions were created. They never include content.
        </li>
      </ul>

      <H>Hosting providers</H>
      <p>The site and its infrastructure run on third-party hosting and storage providers, which process network metadata as part of operating their services.</p>

      <H>Contact</H>
      <p>
        Questions: <a className="text-brand-600 underline dark:text-brand-500" href={`mailto:${CONTACT}`}>{CONTACT}</a>
      </p>
    </Page>
  );
}

export function TermsPage() {
  return (
    <Page title="Terms of use" updated="November 2026">
      <p>By using ephemeral.share you agree to these terms. If you don’t agree, please don’t use the service.</p>

      <H>Don’t upload illegal or harmful content</H>
      <p>You must not use the service to share:</p>
      <ul className="list-disc space-y-1 pl-5">
        <li>child sexual abuse material or any content that exploits minors;</li>
        <li>content that incites or threatens violence, or that harasses or doxxes people;</li>
        <li>malware, phishing material, or anything meant to damage systems or steal data;</li>
        <li>content that infringes copyright or other rights you don’t hold;</li>
        <li>anything else that is unlawful where you or the recipients live.</li>
      </ul>

      <H>What we can and can’t do</H>
      <p>
        Content is stored encrypted and we don’t look at it, so we generally don’t know what is shared. We can, however, end any session and
        delete its files if we receive a credible report or believe the service is being abused, and we may block addresses that abuse it.
        We may cooperate with valid legal requests, but we can only hand over what we hold: encrypted data that is deleted when the timer ends.
      </p>

      <H>Limits</H>
      <p>
        Sessions are temporary and have limits on file size, number of files, storage and participants. Anything you share will be
        deleted automatically — keep your own copy of anything important.
      </p>

      <H>No warranty</H>
      <p>
        The service is provided “as is”, without warranties of any kind. It may be unavailable or change at any time, and you use it at
        your own risk. To the extent permitted by law, we aren’t liable for loss of data or any indirect damages.
      </p>

      <H>Reporting abuse</H>
      <p>
        See <Link to="/abuse" className="text-brand-600 underline dark:text-brand-500">Report abuse</Link>.
      </p>
    </Page>
  );
}

export function AbusePage() {
  return (
    <Page title="Report abuse / takedown" updated="November 2026">
      <p>
        If you’ve seen illegal or harmful content shared through ephemeral.share, or you’re a rights holder, tell us and we’ll act quickly.
      </p>

      <H>How to report</H>
      <p>
        Email{" "}
        <a
          className="text-brand-600 underline dark:text-brand-500"
          href={`mailto:${CONTACT}?subject=Abuse%20report&body=Session%20code%3A%0AWhat%20happened%3A%0A`}
        >
          {CONTACT}
        </a>{" "}
        with:
      </p>
      <ul className="list-disc space-y-1 pl-5">
        <li>
          the <strong>session access code</strong> (this is how we find and end the session — content is stored encrypted, so we can’t search
          by what it contains);
        </li>
        <li>a short description of the problem and, for copyright claims, your contact details and the work concerned.</li>
      </ul>

      <H>What happens next</H>
      <p>
        We’ll end the session and permanently delete its files. Sessions also delete themselves automatically when their timer runs out, so
        content never lingers.
      </p>

      <p className="rounded-lg border border-slate-300 bg-slate-100 px-3 py-2 text-slate-700 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200">
        In an emergency or if someone is in danger, contact your local emergency services first.
      </p>
    </Page>
  );
}
