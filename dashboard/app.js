/* KoLmafia Workspace: intentionally no build step, third-party scripts, or command input. */
(() => {
  'use strict';
  const views = {
    relay: { label: 'Game relay', url: '/game.php' },
    native: { label: 'KoLmafia desktop', url: '/xpra/' },
    inventory: { label: 'Inventory', url: '/inventory.php' },
    character: { label: 'Character', url: '/charsheet.php' },
    quest: { label: 'Quest log', url: '/questlog.php' },
    chat: { label: 'Chat', url: '/chatlaunch.php' },
  };
  const $ = (id) => document.getElementById(id);
  const state = { tabs: [], active: null, nextId: 1, sections: 'game', friendsLoaded: false };
  const setStatus = (message) => { $('status-text').textContent = message; };

  function activate(id) {
    state.active = id;
    for (const tab of state.tabs) {
      tab.button.classList.toggle('active', tab.id === id);
      tab.button.querySelector('[role="tab"]').setAttribute('aria-selected', String(tab.id === id));
      tab.panel.classList.toggle('active', tab.id === id);
      tab.panel.hidden = tab.id !== id;
    }
    for (const button of document.querySelectorAll('[data-open]')) {
      button.classList.toggle('active', state.tabs.some(t => t.id === id && t.key === button.dataset.open));
    }
  }

  function closeTab(id) {
    const idx = state.tabs.findIndex(tab => tab.id === id);
    if (idx === -1) return;
    const wasActive = state.active === id;
    const [tab] = state.tabs.splice(idx, 1);
    tab.button.remove();
    tab.panel.remove();
    if (wasActive) {
      if (state.tabs.length) activate(state.tabs[Math.max(0, idx - 1)].id);
      else openWelcome();
    }
  }

  function createTab(key, label, buildPanel, singleton = false) {
    if (singleton) {
      const found = state.tabs.find(tab => tab.key === key);
      if (found) { activate(found.id); return found; }
    }
    const id = `view-${state.nextId++}`;
    const button = document.createElement('button');
    button.type = 'button'; button.className = 'tab'; button.id = `tab-${id}`;
    button.setAttribute('role', 'tab'); button.setAttribute('aria-controls', `panel-${id}`);
    const title = document.createElement('span'); title.className = 'tab-title'; title.textContent = label;
    const close = document.createElement('button'); close.type = 'button';
    close.className = 'close-tab'; close.textContent = '×'; close.title = `Close ${label}`;
    // Tab close is a nested button visually but never nested in the DOM: use a separate tab wrapper.
    const wrapper = document.createElement('div'); wrapper.className = 'tab';
    wrapper.setAttribute('role', 'presentation'); wrapper.append(button, close);
    button.className = 'tab-main'; button.appendChild(title);
    button.style.cssText = 'border:0;background:transparent;color:inherit;display:flex;align-items:center;min-width:0;flex:1;text-align:left;cursor:pointer;font:inherit';
    button.addEventListener('click', () => activate(id));
    close.addEventListener('click', () => closeTab(id));
    const panel = document.createElement('section'); panel.className = 'panel';
    panel.id = `panel-${id}`; panel.setAttribute('role', 'tabpanel');
    panel.setAttribute('aria-labelledby', `tab-${id}`);
    $('tabs').append(wrapper); $('panels').append(panel);
    const tab = { id, key, button: wrapper, panel };
    state.tabs.push(tab);
    buildPanel(panel);
    // wrapper is the visual selector, while its child is the semantic tab.
    activate(id);
    $('tabs').lastElementChild.scrollIntoView({block:'nearest',inline:'nearest'});
    return tab;
  }

  function openView(key) {
    const view = views[key]; if (!view) return;
    const tab = createTab(key, view.label, panel => {
      const frame = document.createElement('iframe'); frame.src = view.url;
      frame.title = view.label; frame.referrerPolicy = 'same-origin';
      // Keep the iframe active when switched away: the real KoLmafia/Xpra session is stateful.
      panel.append(frame);
      const link = document.createElement('a'); link.className = 'panel-link';
      link.href = view.url; link.target = '_blank'; link.rel = 'noopener noreferrer';
      link.textContent = '↗ Open separately';
      panel.append(link);
    }, true);
    setStatus(`Showing ${tab.key === 'native' ? 'native KoLmafia via Xpra' : view.label}`);
  }

  function openWelcome() {
    createTab('welcome', 'Welcome', panel => {
      const main = document.createElement('div'); main.className = 'welcome';
      const heading = document.createElement('h1'); heading.textContent = 'Your Kingdom of Loathing workspace';
      const intro = document.createElement('p');
      intro.textContent = 'Open the real game relay, the native KoLmafia desktop, or inspect files under the mounted data directory. Keep multiple views in tabs.';
      const launches = document.createElement('div'); launches.className = 'launches';
      for (const [key, title, subtitle] of [
        ['relay', 'Game relay', 'Play through the original relay'],
        ['native', 'KoLmafia desktop', 'Native Swing UI via Xpra'],
        ['files', 'Browse data', 'Read-only scripts and files']
      ]) {
        const launch = document.createElement('button'); launch.className = 'launch';
        const strong = document.createElement('strong'); strong.textContent = title;
        const span = document.createElement('span'); span.textContent = subtitle;
        launch.append(strong, span);
        launch.addEventListener('click', () => key === 'files' ? selectSection('files') : openView(key));
        launches.appendChild(launch);
      }
      main.append(heading, intro, launches); panel.append(main);
    }, true);
  }

  function openFile(path) {
    createTab(`file:${path}`, path.split('/').at(-1), async panel => {
      const view = document.createElement('div'); view.className = 'file-view';
      const h = document.createElement('h2'); h.textContent = path.split('/').at(-1);
      const sub = document.createElement('div'); sub.className = 'sub'; sub.textContent = `data/${path} · read-only`;
      const pre = document.createElement('pre'); pre.textContent = 'Loading…';
      view.append(h, sub, pre); panel.append(view);
      try {
        const res = await fetch(`/_dashboard/api/file?path=${encodeURIComponent(path)}`, { cache: 'no-store' });
        const data = await res.json();
        if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);
        pre.textContent = data.content + (data.truncated ? '\n\n[Preview truncated]' : '');
        setStatus(`Opened data/${path}`);
      } catch (err) {
        pre.textContent = `Unable to open: ${err.message}`; pre.classList.add('error');
        setStatus(`File unavailable: ${path}`);
      }
    }, true);
  }

  async function getTree(path) {
    const res = await fetch(`/_dashboard/api/tree?path=${encodeURIComponent(path)}`, { cache: 'no-store' });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);
    return data.entries;
  }

  function makeTreeItem(item) {
    const container = document.createElement('div'); container.setAttribute('role', 'treeitem');
    const row = document.createElement('div'); row.className = 'tree-row';
    const toggle = document.createElement('button'); toggle.className = 'tree-toggle';
    toggle.type = 'button'; toggle.textContent = item.type === 'directory' ? '▸' : '·';
    toggle.disabled = item.type !== 'directory';
    const name = document.createElement('button'); name.className = 'tree-label';
    name.type = 'button'; name.textContent = `${item.type === 'directory' ? '▤' : '▧'}  ${item.name}`;
    name.title = `data/${item.path}`;
    row.append(toggle, name); container.append(row);
    if (item.type === 'file') name.addEventListener('click', () => openFile(item.path));
    else {
      container.setAttribute('aria-expanded', 'false');
      const children = document.createElement('div'); children.className = 'tree-children hidden';
      children.setAttribute('role', 'group'); container.append(children);
      let loaded = false;
      const expand = async () => {
        const open = children.classList.contains('hidden');
        children.classList.toggle('hidden', !open); toggle.textContent = open ? '▾' : '▸';
        container.setAttribute('aria-expanded', String(open));
        if (!open || loaded) return;
        children.textContent = 'Loading…';
        try {
          const items = await getTree(item.path); children.textContent = '';
          if (!items.length) children.textContent = 'Empty';
          for (const entry of items) children.append(makeTreeItem(entry));
          loaded = true;
        } catch (err) { children.textContent = `Unavailable: ${err.message}`; }
      };
      name.addEventListener('click', expand); toggle.addEventListener('click', expand);
    }
    return container;
  }

  async function loadTree() {
    const tree = $('file-tree'); tree.textContent = 'Loading…';
    try {
      const entries = await getTree(''); tree.textContent = '';
      if (!entries.length) tree.textContent = 'No exposed data folders exist.';
      for (const entry of entries) tree.append(makeTreeItem(entry));
      setStatus('Mounted data tree loaded (read-only)');
    } catch (err) {
      tree.textContent = `Data tree unavailable: ${err.message}`;
      setStatus('Could not load file tree');
    }
  }

  function selectSection(section) {
    state.sections = section;
    document.querySelectorAll('[data-section]').forEach(b => {
      const selected = b.dataset.section === section;
      b.classList.toggle('selected', selected); b.setAttribute('aria-pressed', String(selected));
    });
    for (const name of ['game','files','native']) $(`section-${name}`).classList.toggle('hidden', name !== section);
    if (section === 'files') loadTree();
    if (section === 'native') openView('native');
  }

  function extractFriends(output) {
    // Extract display names from known showplayer links. Do not inject server HTML into the live page.
    const doc = new DOMParser().parseFromString(String(output), 'text/html');
    const names = new Set();
    for (const a of doc.querySelectorAll('a[href]')) {
      const href = a.getAttribute('href') || '';
      if (!/showplayer\.php\?[^#]*\bwho=|showplayer\.php\?[^#]*\bplayerid=/i.test(href)) continue;
      const name = a.textContent.trim().replace(/\s+/g, ' ');
      if (name && name.length <= 80) names.add(name);
    }
    return Array.from(names);
  }

  async function refreshFriends() {
    const button = $('refresh-friends'); button.disabled = true;
    $('friends-heading').textContent = 'ONLINE — CHECKING';
    $('friends-list').textContent = 'Checking friends…';
    try {
      // Only the fixed silent contact query; there is no user-supplied chat input.
      const res = await fetch('/submitnewchat.php?j=1&graf=%2Ffriends', { credentials:'same-origin', cache:'no-store' });
      if (!res.ok) throw new Error(`Relay returned HTTP ${res.status}`);
      const response = await res.json();
      if (!response || typeof response.output !== 'string') throw new Error('No /friends output; login or chat may be unavailable');
      const names = extractFriends(response.output);
      const container = $('friends-list'); container.textContent = '';
      if (names.length) {
        $('friends-heading').textContent = `ONLINE — ${names.length}`;
        for (const name of names) {
          const row = document.createElement('div'); row.className = 'friend-row';
          const avatar = document.createElement('span'); avatar.className = 'friend-avatar'; avatar.textContent = name.charAt(0).toUpperCase();
          const label = document.createElement('span'); label.className = 'friend-name'; label.textContent = name;
          row.append(avatar, label); container.append(row);
        }
      } else {
        $('friends-heading').textContent = 'ONLINE — RESPONSE';
        const p = document.createElement('p'); p.className = 'plain-output';
        p.textContent = new DOMParser().parseFromString(response.output, 'text/html').body.textContent.trim() || 'No friends listed.';
        container.append(p);
      }
      setStatus('Received /friends response');
      state.friendsLoaded = true;
    } catch (err) {
      $('friends-heading').textContent = 'ONLINE — UNAVAILABLE';
      const p = document.createElement('p'); p.className = 'muted error';
      p.textContent = `${err.message}. Log in via the KoLmafia desktop and try again.`;
      $('friends-list').replaceChildren(p);
      setStatus('Friends lookup unavailable');
    } finally { button.disabled = false; }
  }

  function toggleFriends() {
    $('friends-sidebar').classList.toggle('collapsed');
    if (!$('friends-sidebar').classList.contains('collapsed') && !state.friendsLoaded) refreshFriends();
  }

  document.querySelectorAll('[data-section]').forEach(b=>b.addEventListener('click',()=>selectSection(b.dataset.section)));
  document.querySelectorAll('[data-open]').forEach(b=>b.addEventListener('click',()=>openView(b.dataset.open)));
  $('open-relay').addEventListener('click',()=>openView('relay'));
  $('open-native').addEventListener('click',()=>openView('native'));
  $('refresh-files').addEventListener('click',loadTree);
  $('refresh-friends').addEventListener('click',refreshFriends);
  $('toggle-friends').addEventListener('click',toggleFriends);
  $('toggle-friends-rail').addEventListener('click',toggleFriends);
  openWelcome();
  refreshFriends();
})();
