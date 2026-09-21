// The page of aNLpt.
//
// Three rules keep it quick and keep it from fighting whoever is using it:
//
//  1. What somebody changes is shown at once, and sent to the program after. If the
//     program says no, the page says so and takes the real state back.
//  2. A part of the page is only drawn again when what it shows has changed. The page
//     asks how things stand every second; nine times out of ten nothing has, and then
//     nothing is touched: no flicker, no dropdown snapping shut, no focus lost.
//  3. When a part is drawn again, whatever had the focus gets it back.
//
// An earlier version got the second one wrong in the other direction: it refused to draw
// anything while a dropdown or a box had the focus. Focus stays where you last clicked,
// so after one change the page stopped following the program until it was reloaded.
(() => {
  'use strict'

  const $ = id => document.getElementById(id)
  let state = null
  let language = 'nl'
  let signingIn = false
  const drawn = new Map()
  // How many changes are on their way to the program. While there are any, what the
  // program says is a moment out of date, and drawing it would put a switch back to
  // where it was for the blink of an eye before it jumps forward again.
  let saving = 0

  function t(key, values = {}) {
    const text = (window.TEXTS[language] || window.TEXTS.nl)[key] ?? key
    return text.replace(/\{(\w+)\}/g, (_, name) => values[name] ?? '')
  }

  async function call(method, path, body) {
    const response = await fetch(path, {
      method,
      headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
    const data = await response.json().catch(() => ({}))
    if (!response.ok) throw new Error(data.error || 'error_generic')
    return data
  }

  /** A call that changes something: counted, so a refresh waits for it. */
  async function change(method, path, body) {
    saving += 1
    try {
      return await call(method, path, body)
    }
    finally {
      saving -= 1
    }
  }

  function toast(text, kind = '') {
    const node = element('div', { class: `toast ${kind}`, text })
    $('toasts').append(node)
    setTimeout(() => node.classList.add('leaving'), 2600)
    setTimeout(() => node.remove(), 3000)
  }

  function problem(error) {
    const key = `error_${error.message}`
    toast(window.TEXTS[language][key] ? t(key) : t('error_generic'), 'bad')
  }

  function bytes(value) {
    const units = ['B', 'KB', 'MB', 'GB', 'TB']
    let unit = 0
    while (value >= 1024 && unit < units.length - 1) {
      value /= 1024
      unit += 1
    }
    return `${value.toLocaleString(language, { maximumFractionDigits: unit < 2 ? 0 : 1 })} ${units[unit]}`
  }

  function ago(value) {
    if (!value) return t('never')
    const minutes = Math.round((Date.now() - new Date(value).getTime()) / 60000)
    if (minutes < 1) return t('justNow')
    if (minutes < 60) return t('minutesAgo', { n: minutes })
    return new Date(value).toLocaleTimeString(language, { hour: '2-digit', minute: '2-digit' })
  }

  function element(tag, attributes = {}, children = []) {
    const node = document.createElement(tag)
    for (const [name, value] of Object.entries(attributes)) {
      if (name === 'text') node.textContent = value
      else if (name === 'class') node.className = value
      else if (name.startsWith('on')) node.addEventListener(name.slice(2), value)
      else if (value === true) node.setAttribute(name, '')
      else if (value !== false && value != null) node.setAttribute(name, value)
    }
    for (const child of [].concat(children)) if (child) node.append(child)
    return node
  }

  /** Draw a part of the page again, but only if what it shows is different from last time. */
  function paint(id, data, build) {
    const key = JSON.stringify([language, data])
    if (drawn.get(id) === key) return
    drawn.set(id, key)
    const focused = document.activeElement && document.activeElement.id
    $(id).replaceChildren(...[].concat(build()).filter(Boolean))
    if (focused && $(focused) && $(id).contains($(focused))) $(focused).focus({ preventScroll: true })
  }

  function folderName(path) {
    return path.replace(/[\\/]+$/, '').split(/[\\/]/).pop() || path
  }

  // --- changing things: shown at once, sent after ---------------------------------------

  function changeFolder(folder, patch) {
    Object.assign(folder, patch)
    draw()
    change('PUT', `/api/folders/${folder.id}`, folder)
      .then(() => toast(t('saved'), 'good'))
      .catch((error) => {
        problem(error)
        refresh()
      })
  }

  // --- several folders at once -----------------------------------------------------------

  // What the tool is set to. null is "not touched yet", which shows as what the member
  // chose on their profile, and 'leave' keeps what each folder has.
  const several = { open: false, skipped: new Set(), audience: null, name: null, languages: null }

  function severalChoices() {
    const member = state.status.member
    return {
      audience: several.audience || 'members',
      name: several.name || (member && !member.show_holder_name ? 'hide' : 'show'),
    }
  }

  function changeSeveral() {
    const { audience, name } = severalChoices()
    const picked = state.folders.filter(folder => !several.skipped.has(folder.id))
    const patch = { ids: picked.map(folder => folder.id) }
    if (audience !== 'leave') Object.assign(patch, { visibility: audience, downloadable: audience !== 'private' })
    if (name !== 'leave') patch.anonymous = name === 'hide'
    if (several.languages) Object.assign(patch, { language: several.languages[0], other_languages: several.languages.slice(1).sort() })

    for (const folder of picked) {
      const { ids, ...mine } = patch
      Object.assign(folder, mine)
      folder.other_languages = (folder.other_languages || []).filter(code => code !== folder.language)
    }
    several.open = false
    draw()
    change('PUT', '/api/folders', patch)
      .then(() => toast(t('severalSaved', { count: picked.length }), 'good'))
      .catch((error) => {
        problem(error)
        refresh()
      })
  }

  function choice(id, label, options, current, pick, hint) {
    return element('div', { class: 'field' }, [
      element('span', { class: 'label', text: label }),
      element('div', { 'class': 'segments', 'role': 'group', 'aria-label': label },
        options.map(([value, text]) => element('button', {
          'id': `${id}-${value}`,
          'type': 'button',
          'class': current === value ? 'on' : '',
          'aria-pressed': String(current === value),
          'text': text,
          'onclick': () => {
            pick(value)
            draw()
          },
        }))),
      hint ? element('p', { class: 'hint', text: hint }) : null,
    ])
  }

  function drawSeveral() {
    const member = state.status.member
    const shown = [
      state.folders.map(folder => [folder.id, folder.path]), several.open, [...several.skipped], several.audience,
      several.name, several.languages, member && member.show_holder_name,
    ]
    paint('several', shown, () => {
      if (state.folders.length < 2) return null
      const { audience, name } = severalChoices()
      const picked = state.folders.filter(folder => !several.skipped.has(folder.id))
      const leave = ['leave', t('severalLeave')]

      return element('details', {
        class: 'several',
        open: several.open,
        ontoggle: (event) => {
          if (several.open === event.target.open) return
          several.open = event.target.open
          draw()
        },
      }, [
        element('summary', { id: 'several-summary' }, [
          element('span', { text: t('severalTitle') }),
          element('small', { text: t('severalText') }),
        ]),
        element('div', { class: 'several-body' }, [
          element('div', { class: 'field' }, [
            element('div', { class: 'row wrap' }, [
              element('span', { class: 'label grow', text: t('severalWhich', { count: picked.length, all: state.folders.length }) }),
              element('button', { id: 'several-all', class: 'ghost small', type: 'button', text: t('severalAll'), onclick: () => {
                several.skipped.clear()
                draw()
              } }),
              element('button', { id: 'several-none', class: 'ghost small', type: 'button', text: t('severalNone'), onclick: () => {
                several.skipped = new Set(state.folders.map(folder => folder.id))
                draw()
              } }),
            ]),
            element('div', { class: 'chips' }, state.folders.map(folder => element('button', {
              'id': `several-folder-${folder.id}`,
              'type': 'button',
              'class': several.skipped.has(folder.id) ? 'chip' : 'chip on',
              'aria-pressed': String(!several.skipped.has(folder.id)),
              'title': folder.path,
              'text': folderName(folder.path),
              'onclick': () => {
                if (several.skipped.has(folder.id)) several.skipped.delete(folder.id)
                else several.skipped.add(folder.id)
                draw()
              },
            }))),
          ]),
          element('div', { class: 'grid' }, [
            choice('several-audience', t('visibility'),
              [leave, ...['public', 'members', 'private'].map(key => [key, t(`visibility_${key}`)])],
              audience, (value) => { several.audience = value },
              audience === 'leave' ? '' : t(`visibilityHint_${audience}`)),
            choice('several-name', t('severalName'),
              [leave, ['show', t('severalNameShow')], ['hide', t('severalNameHide')]],
              name, (value) => { several.name = value },
              several.name ? '' : t('severalFromProfile')),
          ]),
          element('div', { class: 'field' }, [
            element('span', { class: 'label', text: t('spokenLanguage') }),
            switchControl('several-languages', Boolean(several.languages), t('severalLanguages'), (value) => {
              several.languages = value ? ['nl'] : null
              draw()
            }, t('severalLanguagesHint')),
            several.languages
              ? languagePicker('several-spoken', several.languages, (all) => {
                  several.languages = all
                  draw()
                })
              : null,
          ]),
          element('div', { class: 'row wrap' }, [
            element('button', {
              id: 'several-apply',
              class: 'primary',
              type: 'button',
              disabled: !picked.length
                || (audience === 'leave' && name === 'leave' && !several.languages),
              text: t('severalApply', { count: picked.length }),
              onclick: changeSeveral,
            }),
            element('button', {
              id: 'several-reset',
              class: 'ghost',
              type: 'button',
              text: t('severalReset'),
              onclick: () => {
                Object.assign(several, { audience: null, name: null, languages: null })
                draw()
              },
            }),
          ]),
        ]),
      ])
    })
  }

  function changeSetting(patch, quiet = false) {
    Object.assign(state, patch)
    draw()
    change('PUT', '/api/settings', patch)
      .then(() => quiet || toast(t('saved'), 'good'))
      .catch((error) => {
        problem(error)
        refresh()
      })
  }

  // --- the parts ------------------------------------------------------------------------

  /**
   * What something is spoken in: as many languages as there are tracks, the first of them
   * the main one. A recording with a Dutch and an English track is both, and a list to
   * pick one from could only ever say half of that.
   */
  function languagePicker(id, chosen, onchange) {
    const names = new Map(window.COPY_LANGUAGES)
    const rest = window.COPY_LANGUAGES.filter(([code]) => !chosen.includes(code) && code !== 'zz')
    return element('div', { class: 'picker' }, [
      ...chosen.map((code, index) => element('span', { class: index ? 'chip on lang' : 'chip on lang main' }, [
        element('button', {
          id: `${id}-${code}`,
          type: 'button',
          class: 'plain',
          title: index ? t('languageMakeMain') : t('languageMain'),
          onclick: () => index && onchange([code, ...chosen.filter(other => other !== code)]),
        }, [
          element('span', { text: names.get(code) || code }),
          index ? null : element('small', { text: t('languageMain') }),
        ]),
        chosen.length > 1
          ? element('button', {
              'id': `${id}-${code}-remove`,
              'type': 'button',
              'class': 'plain x',
              'aria-label': t('languageRemove', { language: names.get(code) || code }),
              'text': '×',
              'onclick': () => onchange(chosen.filter(other => other !== code)),
            })
          : null,
      ])),
      rest.length
        ? element('select', {
            'id': `${id}-add`,
            'class': 'add-language',
            'aria-label': t('languageAdd'),
            'onchange': event => event.target.value && onchange([...chosen, event.target.value]),
          }, [['', t('languageAdd')], ...rest].map(([code, text]) => element('option', { value: code, text })))
        : null,
    ])
  }

  /** The languages of a folder or a rule as the picker has them: the main one first. */
  function spoken(thing) {
    return [thing.language, ...(thing.other_languages || [])]
  }

  function switchControl(id, checked, label, onchange, hint) {
    return element('label', { class: 'switch', for: id }, [
      element('input', { id, type: 'checkbox', checked, onchange: event => onchange(event.target.checked) }),
      element('span', { class: 'track' }),
      element('span', { class: 'switch-text' }, [
        element('span', { text: label }),
        hint ? element('small', { text: hint }) : null,
      ]),
    ])
  }

  function drawFolder(folder) {
    // Who may download what is in here: three answers, so three buttons. Nothing else
    // hangs on it. Who may see the copy on the site is not decided here.
    const audience = folder.downloadable === false ? 'private' : folder.visibility
    const visibility = element('div', { 'class': 'segments', 'role': 'group', 'aria-label': t('visibility') },
      ['public', 'members', 'private'].map(key => element('button', {
        'id': `visibility-${folder.id}-${key}`,
        'type': 'button',
        'class': audience === key ? 'on' : '',
        'aria-pressed': String(audience === key),
        'text': t(`visibility_${key}`),
        'onclick': () => audience !== key
          && changeFolder(folder, { visibility: key, downloadable: key !== 'private' }),
      })))

    const tree = treeOf(folder)
    const settings = element('div', { class: 'folder-settings' }, [
      element('div', { class: 'folder-head' }, [
        element('div', {}, [
          element('h3', { text: folderName(folder.path) }),
          element('p', { class: 'path', text: folder.path }),
        ]),
        element('button', {
          class: 'ghost danger small',
          type: 'button',
          text: t('remove'),
          onclick: () => {
            if (!window.confirm(t('confirmRemove'))) return
            state.folders = state.folders.filter(other => other.id !== folder.id)
            draw()
            change('DELETE', `/api/folders/${folder.id}`).catch((error) => {
              problem(error)
              refresh()
            })
          },
        }),
      ]),
      element('div', { class: 'field' }, [
        element('span', { class: 'label', text: t('visibility') }),
        visibility,
        element('p', { class: 'hint', text: t(`visibilityHint_${audience}`) }),
      ]),
      element('div', { class: 'field' }, [
        element('span', { class: 'label', text: t('spokenLanguage') }),
        languagePicker(`spoken-${folder.id}`, spoken(folder), all => changeFolder(folder, {
          language: all[0],
          other_languages: all.slice(1).sort(),
        })),
        element('p', { class: 'hint', text: t('languagesHint') }),
      ]),
      element('div', { class: 'switches' }, [
        switchControl(`anonymous-${folder.id}`, folder.anonymous, t('anonymous'),
          value => changeFolder(folder, { anonymous: value }), t('anonymousHint')),
      ]),
    ])

    // What is in it, always there: somebody who shares everything they have shares one
    // folder, and what they want to set is in here. The search box and the two places
    // below it are made once; what changes while ticking is drawn into them (drawTree).
    const contents = element('div', { class: 'folder-tree' }, [
      element('h4', { text: t('treeTitle') }),
      element('p', { class: 'hint', text: t('treeText') }),
      element('div', { class: 'tree-bar' }, [
        element('input', {
          id: `tree-filter-${folder.id}`,
          type: 'search',
          autocomplete: 'off',
          spellcheck: 'false',
          placeholder: t('treeFilter'),
          value: tree.filter,
          oninput: (event) => {
            tree.filter = event.target.value
            draw()
          },
        }),
        element('div', { id: `tree-actions-${folder.id}`, class: 'row wrap' }),
      ]),
      element('div', { id: `tree-rows-${folder.id}`, class: 'tree' }),
    ])
    return element('article', { class: 'folder' }, [settings, contents])
  }

  // --- inside a folder: series, seasons, episodes ------------------------------------------

  // What is in each folder comes from this computer, and is asked for again whenever the
  // folders were looked through; who may fetch what is part of the folder's settings.
  const trees = new Map()

  function treeOf(folder) {
    if (!trees.has(folder.id)) trees.set(folder.id, { root: null, open: new Set(), picked: new Set(), filter: '', seen: null, languages: null })
    return trees.get(folder.id)
  }

  /** Files as a tree of directories, the way they are on the disk. */
  function grow(files) {
    const root = { path: '', dirs: new Map(), files: [], count: 0, size: 0 }
    for (const file of files) {
      const parts = file.path.split('/')
      let node = root
      root.count += 1
      root.size += file.size
      parts.slice(0, -1).forEach((name, depth) => {
        if (!node.dirs.has(name)) {
          node.dirs.set(name, { name, path: parts.slice(0, depth + 1).join('/'), dirs: new Map(), files: [], count: 0, size: 0 })
        }
        node = node.dirs.get(name)
        node.count += 1
        node.size += file.size
      })
      node.files.push({ name: parts[parts.length - 1], path: file.path, size: file.size })
    }
    return root
  }

  async function loadTree(folder, scan) {
    const tree = treeOf(folder)
    tree.seen = scan
    try {
      const found = await call('GET', `/api/folders/${folder.id}/files`)
      const first = !tree.root
      tree.root = grow(found.files)
      // One series in the folder: show its seasons at once.
      if (first && tree.root.dirs.size === 1 && !tree.root.files.length) tree.open.add([...tree.root.dirs.values()][0].path)
      draw()
    }
    catch (error) {
      problem(error)
    }
  }

  function inside(path, above) {
    return path === above || path.startsWith(`${above}/`)
  }

  function setAudience(folder, visibility) {
    const tree = treeOf(folder)
    const paths = [...tree.picked]
    if (!paths.length) return
    // The same as the program does with it, so it shows at once: who may fetch it goes from
    // everything inside what was ticked, and what those are spoken in stays.
    const rules = new Map()
    for (const rule of folder.rules || []) {
      const kept = paths.some(path => inside(rule.path, path)) ? { ...rule, visibility: undefined } : rule
      if (kept.visibility || kept.language) rules.set(kept.path, kept)
    }
    if (visibility) for (const path of paths) rules.set(path, { ...(rules.get(path) || { path }), visibility })
    folder.rules = [...rules.values()]
    tree.picked.clear()
    draw()
    change('PUT', `/api/folders/${folder.id}/audience`, { paths, visibility })
      .then(() => toast(t('saved'), 'good'))
      .catch((error) => {
        problem(error)
        refresh()
      })
  }

  function setLanguages(folder, languages) {
    const tree = treeOf(folder)
    const paths = [...tree.picked]
    if (!paths.length) return
    const [language = '', ...others] = languages || []
    // The same as the program does with it, so it shows at once.
    const rules = new Map((folder.rules || []).map(rule => [rule.path, { ...rule }]))
    for (const path of paths) {
      const rule = rules.get(path) || { path }
      delete rule.language
      delete rule.other_languages
      if (language) Object.assign(rule, { language, other_languages: others.sort() })
      if (rule.visibility || rule.language) rules.set(path, rule)
      else rules.delete(path)
    }
    folder.rules = [...rules.values()]
    tree.picked.clear()
    tree.languages = null
    draw()
    change('PUT', `/api/folders/${folder.id}/languages`, { paths, language, other_languages: others })
      .then(() => toast(t('saved'), 'good'))
      .catch((error) => {
        problem(error)
        refresh()
      })
  }

  function drawTree(folder) {
    const tree = treeOf(folder)
    // Looked through again, or for the first time: what is in it may be different now.
    const scan = `${state.status.last_scan}|${Boolean(state.status.scanning)}`
    if (tree.seen !== scan) loadTree(folder, scan)

    const rules = new Map((folder.rules || []).filter(rule => rule.visibility).map(rule => [rule.path, rule.visibility]))
    const tongues = new Map((folder.rules || []).filter(rule => rule.language).map(rule => [rule.path, spoken(rule)]))
    // Languages belong to a series: the archive keeps them per copy, and an episode has none.
    const deeper = [...tree.picked].some(path => path.includes('/'))
    const languages = tree.languages || spoken(folder)
    const ofFolder = folder.downloadable === false ? 'private' : folder.visibility
    const wanted = tree.filter.trim().toLowerCase()
    const top = tree.root
      ? [...tree.root.dirs.values(), ...tree.root.files].filter(entry => !wanted || entry.name.toLowerCase().includes(wanted))
      : []

    const different = (folder.rules || []).length
    paint(`tree-actions-${folder.id}`, [tree.picked.size, top.length, Boolean(tree.root), different, deeper, languages], () => [
      element('span', { class: 'muted small grow', text: tree.picked.size || !different
        ? t('treePicked', { count: tree.picked.size })
        : t('treeRules', { count: different }) }),
      element('button', { id: `tree-all-${folder.id}`, class: 'ghost small', type: 'button', text: t('treeAll'), disabled: !top.length, onclick: () => {
        tree.picked = new Set(top.map(entry => entry.path))
        draw()
      } }),
      element('button', { id: `tree-none-${folder.id}`, class: 'ghost small', type: 'button', text: t('severalNone'), disabled: !tree.picked.size, onclick: () => {
        tree.picked.clear()
        draw()
      } }),
      element('div', { 'class': 'segments', 'role': 'group', 'aria-label': t('visibility') },
        [['', t('treeAsAbove')], ...['public', 'members', 'private'].map(key => [key, t(`visibility_${key}`)])]
          .map(([key, text]) => element('button', {
            id: `tree-set-${folder.id}-${key || 'above'}`,
            type: 'button',
            disabled: !tree.picked.size,
            text,
            onclick: () => setAudience(folder, key),
          }))),
      tree.picked.size
        ? element('div', { class: 'tree-languages' }, deeper
            ? [element('p', { class: 'hint', text: t('treeLanguagesPerSeries') })]
            : [
                element('span', { class: 'label', text: t('spokenLanguage') }),
                languagePicker(`tree-spoken-${folder.id}`, languages, (all) => {
                  tree.languages = all
                  draw()
                }),
                element('button', { id: `tree-languages-set-${folder.id}`, class: 'primary small', type: 'button', text: t('treeLanguagesSet'), onclick: () => setLanguages(folder, languages) }),
                element('button', { id: `tree-languages-folder-${folder.id}`, class: 'ghost small', type: 'button', text: t('treeLanguagesFolder'), onclick: () => setLanguages(folder, null) }),
              ])
        : null,
    ])

    const shown = [
      [...rules], [...tongues], ofFolder, wanted, [...tree.open], [...tree.picked], tree.root ? tree.root.count : -1,
    ]
    paint(`tree-rows-${folder.id}`, shown, () => {
      if (!tree.root) return element('p', { class: 'empty', text: t('treeLoading') })
      if (!top.length) return element('p', { class: 'empty', text: t(tree.root.count ? 'treeNothingFound' : 'treeEmpty') })
      const rows = []
      const add = (entry, depth, above, abovePicked) => {
        const isDir = Boolean(entry.dirs)
        const own = rules.get(entry.path)
        const audience = own || above
        const picked = abovePicked || tree.picked.has(entry.path)
        const opened = isDir && tree.open.has(entry.path)
        const key = rows.length
        rows.push(element('div', { class: `tree-row d${Math.min(depth, 8)} ${picked ? 'picked' : ''}` }, [
          element('input', {
            'id': `pick-${folder.id}-${key}`,
            'type': 'checkbox',
            'checked': picked,
            'disabled': abovePicked,
            'aria-label': entry.name,
            'onchange': (event) => {
              if (event.target.checked) {
                // The whole of it: whatever was picked inside it comes along anyway.
                for (const path of [...tree.picked]) if (inside(path, entry.path)) tree.picked.delete(path)
                tree.picked.add(entry.path)
              }
              else {
                tree.picked.delete(entry.path)
              }
              draw()
            },
          }),
          isDir
            ? element('button', {
                'id': `open-${folder.id}-${key}`,
                'type': 'button',
                'class': 'tree-name',
                'aria-expanded': String(opened),
                'onclick': () => {
                  if (opened) tree.open.delete(entry.path)
                  else tree.open.add(entry.path)
                  draw()
                },
              }, [
                element('span', { class: `caret ${opened ? 'down' : ''}`, text: '▸' }),
                element('span', { text: entry.name }),
              ])
            : element('span', { class: 'tree-name file', text: entry.name }),
          element('span', { class: 'muted small', text: isDir ? t('treeCount', { count: entry.count, size: bytes(entry.size) }) : bytes(entry.size) }),
          tongues.has(entry.path)
            ? element('span', { class: 'pill tongues', title: t('spokenLanguage'), text: tongues.get(entry.path).map(code => code.toUpperCase()).join(' + ') })
            : null,
          element('span', { class: `pill audience ${own ? audience : 'follows'}`, text: t(`visibility_${audience}`) }),
        ]))
        if (!opened) return
        for (const dir of entry.dirs.values()) add(dir, depth + 1, audience, picked)
        for (const file of entry.files) add(file, depth + 1, audience, picked)
      }
      for (const entry of top) add(entry, 0, ofFolder, false)
      return rows
    })
  }

  /** How long is left, in words: "2 dagen en 23 uur". */
  function remaining(until) {
    const minutes = Math.max(0, Math.round((new Date(until).getTime() - Date.now()) / 60000))
    const days = Math.floor(minutes / 1440)
    const hours = Math.floor((minutes % 1440) / 60)
    if (days > 0) return t('leftDaysHours', { days, hours })
    if (hours > 0) return t('leftHours', { hours, minutes: minutes % 60 })
    return t('leftMinutes', { minutes })
  }

  /** Downloading still works, but only because of the days of grace. */
  function lapsing(member) {
    return Boolean(member && member.may_download && !member.is_sharing && member.download_ends_at)
  }

  // Somebody who stopped sharing keeps downloading for a few days, and then loses it. That
  // used to be a green "Je mag downloaden" with the rest in small grey print beside it,
  // which reads as "all is well" and was read that way. It is the one thing on this page
  // somebody has to act on, so it is a card of its own, at the top, in the colour of a
  // warning, with how long is left and what to do about it.
  function drawGrace(status) {
    const member = status.member
    $('grace').hidden = !lapsing(member)
    if (!lapsing(member)) return
    const left = remaining(member.download_ends_at)
    paint('grace', [left, member.download_ends_at], () => [
      element('div', { class: 'grace-clock' }, [
        element('span', { class: 'grace-label', text: t('graceLeft') }),
        element('strong', { text: left }),
      ]),
      element('div', { class: 'grace-text' }, [
        element('h2', { text: t('graceTitle') }),
        element('p', { text: t('graceText', {
          date: new Date(member.download_ends_at).toLocaleString(language, { dateStyle: 'full', timeStyle: 'short' }),
        }) }),
        element('button', {
          class: 'primary',
          type: 'button',
          text: t('graceAction'),
          onclick: () => {
            $('folder-path').scrollIntoView({ block: 'center' })
            $('folder-path').focus()
          },
        }),
      ]),
    ])
  }

  function drawMember(status) {
    const member = status.member
    // The time that is left is part of what is shown, so it is part of what is compared.
    paint('member', [member, status.state, lapsing(member) && remaining(member.download_ends_at)], () => {
      if (!member && status.state === 'revoked') {
        return element('div', {}, [
          element('p', { class: 'muted', text: t('revokedWaiting') }),
          element('button', {
            class: 'ghost small',
            type: 'button',
            text: t('tryAgain'),
            onclick: (event) => {
              // Once, and the answer shows by itself: the page already asks how things are.
              event.currentTarget.disabled = true
              call('POST', '/api/try-again').catch(problem)
            },
          }),
        ])
      }
      if (!member) return element('p', { class: 'muted', text: t('state_starting') })
      let why = t('downloadHow')
      let pill = member.may_download ? ['good', t('downloadYes')] : ['off', t('downloadNo')]
      if (member.is_sharing) why = t('downloadSharing')
      else if (lapsing(member)) {
        // Not green: it works today and stops on a day that is already known.
        pill = ['warn', t('downloadEnding', { left: remaining(member.download_ends_at) })]
        why = t('downloadEndingWhy')
      }
      else if (member.may_download) why = ''
      const name = member.display_name || member.username
      return [
        element('div', { class: 'avatar', text: name.slice(0, 1).toUpperCase() }),
        element('div', { class: 'who' }, [
          element('h2', { text: name }),
          element('p', { class: 'muted', text: t('level', { level: member.level, xp: member.xp.toLocaleString(language) }) }),
        ]),
        element('div', { class: 'download' }, [
          element('span', { class: `pill ${pill[0]}`, text: pill[1] }),
          why ? element('p', { class: 'muted small', text: why }) : null,
        ]),
      ]
    })
  }

  function drawStats(status) {
    const directories = status.directories || []
    const episodes = directories.reduce((sum, row) => sum + (row.episodes || 0), 0)
    const data = [status.files, status.bytes, episodes, status.sent, ago(status.last_scan), status.scanning]
    paint('stats', data, () => [
      ['statFiles', status.files.toLocaleString(language), bytes(status.bytes)],
      ['statEpisodes', episodes.toLocaleString(language), t('statEpisodesHint')],
      ['statSent', status.sent.toLocaleString(language), t('statSentHint')],
      ['statScan', status.scanning ? t('state_scanning') : ago(status.last_scan), t('statScanHint', { n: state.scan_minutes })],
    ].map(([label, value, hint]) => element('div', { class: 'stat' }, [
      element('span', { class: 'stat-label', text: t(label) }),
      element('strong', { text: value }),
      element('span', { class: 'stat-hint', text: hint }),
    ])))
  }

  function drawDirectories(status) {
    const all = status.directories || []
    const unmatched = all.filter(row => row.match_state === 'unmatched')
    const recognised = all.filter(row => row.match_state === 'auto' || row.match_state === 'manual')

    $('unmatched-card').hidden = unmatched.length === 0
    // Each one leads somewhere that does something about it. Most of what is not
    // recognised is simply not in the catalogue yet, so the first button opens the page
    // that adds a title, with this one's name already looked up. The other is for a
    // folder whose name the archive could not make sense of but whose title it has.
    const open = path => call('POST', '/api/open', { path }).catch(problem)
    paint('unmatched-list', unmatched, () => unmatched.map(row => element('div', { class: 'line actions-line' }, [
      element('div', { class: 'line-main' }, [
        element('span', { text: row.name }),
        element('small', { class: 'muted', text: t('filmLine', { files: row.files }) }),
      ]),
      element('div', { class: 'row wrap' }, [
        element('button', {
          id: `add-${row.id}`,
          class: 'primary small',
          type: 'button',
          text: t('addTitle'),
          onclick: () => open(`/titles/new?q=${encodeURIComponent(row.search || row.name)}`),
        }),
        element('button', {
          id: `pick-${row.id}`,
          class: 'ghost small',
          type: 'button',
          text: t('pickTitle'),
          onclick: () => open('/me/shares'),
        }),
      ]),
    ])))

    $('recognised-card').hidden = recognised.length === 0
    paint('recognised-list', recognised, () => recognised.map(row => element('div', { class: 'line' }, [
      element('div', { class: 'line-main' }, [
        element('span', { text: row.title ? row.title.name : row.name }),
        row.unread && row.unread.length
          ? element('small', { class: 'muted', text: t('unreadLine', { names: row.unread.slice(0, 4).join(', ') }) })
          : null,
      ]),
      element('span', {
        class: 'muted',
        text: row.episodes
          ? t('recognisedLine', { episodes: row.episodes, files: row.files })
          : t('filmLine', { files: row.files }),
      }),
    ])))
  }

  // What is being sent and what became of what was: one table at the bottom, a row each.
  // It used to be a card per file near the top, which is fine for one file and a wall for
  // ten, and it only ever showed files on their way: one that was cancelled simply sat
  // there. The program keeps the last twenty for a day, in memory, so there is never more
  // than a screenful and never anything to page through.
  function drawTransfers(status) {
    const sending = status.sending || []
    const history = status.history || []
    $('transfers-card').hidden = sending.length + history.length === 0
    const clock = value => new Date(value).toLocaleTimeString(language, { hour: '2-digit', minute: '2-digit' })

    paint('transfers', [sending, history], () => [
      ...sending.map((item) => {
        const percent = Math.round(item.done / Math.max(1, item.pieces) * 100)
        return element('tr', { class: 'now' }, [
          element('td', { class: 'file', text: item.name || '…' }),
          element('td', {}, [
            element('div', { class: 'progress-cell' }, [
              element('progress', { max: item.pieces, value: item.done }),
              element('span', { class: 'muted small', text: `${percent}%` }),
            ]),
          ]),
          element('td', { class: 'num', text: bytes(item.size || 0) }),
          element('td', { class: 'num muted', text: clock(item.started) }),
        ])
      }),
      ...history.map(item => element('tr', {}, [
        element('td', { class: 'file', text: item.name || '…' }),
        element('td', {}, [
          element('span', { class: `tag ${item.outcome}`, text: t(`outcome_${item.outcome}`) }),
          item.reason ? element('span', { class: 'muted small reason', text: t(`reason_${item.reason}`) }) : null,
        ]),
        element('td', { class: 'num', text: bytes(item.size || 0) }),
        element('td', { class: 'num muted', text: clock(item.ended) }),
      ])),
    ])
  }

  function drawSettings(status) {
    const data = [state.autostart, status.state === 'paused', state.scan_minutes, state.upload_kbps]
    paint('settings', data, () => [
      switchControl('autostart', state.autostart, t('autostart'), value => changeSetting({ autostart: value })),
      switchControl('paused', status.state === 'paused', t('pause'), (value) => {
        state.status.state = value ? 'paused' : 'sharing'
        draw()
        change('POST', '/api/pause', { paused: value }).catch((error) => {
          problem(error)
          refresh()
        })
      }),
      element('div', { class: 'grid' }, [
        element('label', { class: 'field' }, [
          element('span', { class: 'label', text: t('scanEvery') }),
          element('input', {
            id: 'scan-minutes',
            type: 'number',
            min: 5,
            max: 1440,
            step: 5,
            value: state.scan_minutes,
            onchange: event => changeSetting({ scan_minutes: Math.max(5, Number(event.target.value) || 30) }),
          }),
        ]),
        element('label', { class: 'field' }, [
          element('span', { class: 'label', text: t('uploadCap') }),
          element('input', {
            id: 'upload-kbps',
            type: 'number',
            min: 0,
            step: 100,
            value: state.upload_kbps,
            onchange: event => changeSetting({ upload_kbps: Math.max(0, Number(event.target.value) || 0) }),
          }),
        ]),
      ]),
    ])
  }

  function drawTexts() {
    if (drawn.get('texts') === language) return
    drawn.set('texts', language)
    document.documentElement.lang = language
    for (const node of document.querySelectorAll('[data-t]')) node.textContent = t(node.dataset.t)
    $('lang').textContent = t('language')
    $('folder-path').placeholder = t('pathPlaceholder')
  }

  function draw() {
    if (!state) return
    drawTexts()
    const status = state.status
    const signedIn = status.state !== 'signed_out'
    $('welcome').hidden = signedIn
    $('app').hidden = !signedIn
    $('waiting').hidden = !signingIn || signedIn
    $('sign-in').disabled = signingIn && !signedIn
    if (signedIn) signingIn = false

    const shown = status.scanning && status.state === 'sharing' ? 'scanning' : status.state
    $('state-text').textContent = t(`state_${shown}`)
    const mood = status.state === 'sharing' ? 'good' : status.state === 'offline' || status.state === 'revoked' ? 'bad' : ''
    $('state').className = `status ${mood} ${shown === 'scanning' ? 'busy' : ''}`
    $('rescan-icon').classList.toggle('spin', Boolean(status.scanning))

    const banner = $('banner')
    banner.hidden = !(status.state === 'offline' || status.state === 'revoked')
    banner.textContent = status.state === 'revoked' ? t('revokedBanner') : t('offlineBanner')

    $('version').textContent = `aNLpt ${state.version}`
    $('server').textContent = state.server
    if (!signedIn) return

    $('autostart-question').hidden = state.autostart_asked
    drawGrace(status)
    drawMember(status)
    drawStats(status)
    drawDirectories(status)
    drawSettings(status)

    drawSeveral()
    // The cards are made again when a setting changes, not when something is ticked in a
    // tree: that is drawn into the card, so the search box keeps its place and its text.
    const cards = state.folders.map(({ rules, ...settings }) => settings)
    paint('folder-list', [cards], () => {
      for (const folder of state.folders) {
        drawn.delete(`tree-actions-${folder.id}`)
        drawn.delete(`tree-rows-${folder.id}`)
      }
      return state.folders.length
        ? state.folders.map(drawFolder)
        : element('p', { class: 'empty', text: t('noFolders') })
    })
    for (const folder of state.folders) drawTree(folder)
    for (const id of [...trees.keys()]) if (!state.folders.some(folder => folder.id === id)) trees.delete(id)
    $('browse').hidden = !state.can_pick

    drawTransfers(status)
  }

  let refreshing = false
  async function refresh() {
    if (refreshing) return
    refreshing = true
    try {
      const fresh = await call('GET', '/api/state')
      if (saving) return
      language = fresh.language
      state = fresh
      document.body.classList.remove('gone')
      draw()
    }
    catch {
      // The program was closed. Say so rather than showing a page that does nothing.
      document.body.classList.add('gone')
      $('state-text').textContent = t('programClosed')
      $('state').className = 'status bad'
    }
    finally {
      refreshing = false
    }
  }

  function showFolderError(code) {
    const node = $('folder-error')
    node.hidden = !code
    const key = `error_${code}`
    node.textContent = code ? (window.TEXTS[language][key] ? t(key) : t('error_generic')) : ''
  }

  $('lang').addEventListener('click', () => {
    language = language === 'nl' ? 'en' : 'nl'
    changeSetting({ language }, true)
  })
  $('sign-in').addEventListener('click', () => {
    signingIn = true
    draw()
    call('POST', '/api/sign-in').catch(problem)
  })
  $('sign-out').addEventListener('click', () => {
    if (window.confirm(t('confirmSignOut'))) call('POST', '/api/sign-out').then(refresh).catch(problem)
  })
  $('rescan').addEventListener('click', () => {
    state.status.scanning = true
    draw()
    call('POST', '/api/rescan').then(refresh).catch(problem)
  })
  $('open-site').addEventListener('click', () => call('POST', '/api/open', { path: '/' }).catch(problem))
  $('autostart-yes').addEventListener('click', () => changeSetting({ autostart: true, autostart_asked: true }))
  $('autostart-no').addEventListener('click', () => changeSetting({ autostart: false, autostart_asked: true }, true))
  $('browse').addEventListener('click', async () => {
    try {
      const picked = await call('POST', '/api/pick-folder')
      if (picked.path) {
        $('folder-path').value = picked.path
        $('share').focus()
      }
    }
    catch { /* closed without choosing */ }
  })
  $('add-folder').addEventListener('submit', async (event) => {
    event.preventDefault()
    showFolderError('')
    $('share').disabled = true
    try {
      const folder = await call('POST', '/api/folders', { path: $('folder-path').value })
      state.folders = [...state.folders, folder]
      state.status.scanning = true
      $('folder-path').value = ''
      draw()
      toast(t('folderAdded'), 'good')
    }
    catch (error) {
      showFolderError(error.message)
    }
    finally {
      $('share').disabled = false
    }
  })

  refresh()
  setInterval(refresh, 1000)
  // Back from the browser tab where signing in happened: look at once, not in a second.
  document.addEventListener('visibilitychange', () => document.hidden || refresh())
  window.addEventListener('focus', refresh)
})()
