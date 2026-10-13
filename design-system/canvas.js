/* Canvas helpers (design system). A classic script: defines window.Canvas.
 *   Canvas.chart(el, config)  Chart.js with token colors and fonts; returns the Chart.
 *   Canvas.token(name)        the current value of a CSS custom property, e.g. '--c-accent'.
 *   Canvas.palette()          the six categorical chart colors, in order.
 *   Canvas.ready              a Promise that settles after every <pre class="mermaid"> is rendered.
 * Pages are static snapshots: when the system switches light/dark, the page reloads so
 * charts and diagrams pick up the other palette. */
(function () {
  'use strict';

  var PALETTE = ['--chart-1', '--chart-2', '--chart-3', '--chart-4', '--chart-5', '--chart-6'];
  var PER_POINT = { pie: true, doughnut: true, polarArea: true };

  function token(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }

  function palette() {
    return PALETTE.map(token);
  }

  function isDark() {
    return window.matchMedia('(prefers-color-scheme: dark)').matches;
  }

  /* '#rrggbb' -> 'rgba(r, g, b, a)'; anything else is returned unchanged. */
  function alpha(hex, a) {
    var m = /^#([0-9a-f]{6})$/i.exec(hex);
    if (!m) return hex;
    var n = parseInt(m[1], 16);
    return 'rgba(' + ((n >> 16) & 255) + ', ' + ((n >> 8) & 255) + ', ' + (n & 255) + ', ' + a + ')';
  }

  function problem(el, message) {
    var box = document.createElement('div');
    box.className = 'callout callout-bad';
    box.textContent = message;
    if (el && el.parentNode) el.parentNode.insertBefore(box, el);
    else document.body.appendChild(box);
    console.error('[canvas] ' + message);
  }

  function themeChartDefaults(C) {
    C.defaults.color = token('--c-text-muted');
    C.defaults.borderColor = token('--c-border');
    C.defaults.font.family = token('--font-sans');
    C.defaults.font.size = 12;
    C.defaults.animation = false;
    C.defaults.maintainAspectRatio = false;
    C.defaults.plugins.legend.labels.usePointStyle = true;
    C.defaults.plugins.legend.labels.boxWidth = 8;
    C.defaults.plugins.legend.labels.boxHeight = 8;
    C.defaults.plugins.tooltip.backgroundColor = token('--c-text');
    C.defaults.plugins.tooltip.titleColor = token('--c-surface');
    C.defaults.plugins.tooltip.bodyColor = token('--c-surface');
  }

  /* Fills in colors the author left out; explicit colors in the config always win. */
  function colorize(config) {
    var pal = palette();
    var data = config.data || {};
    (data.datasets || []).forEach(function (ds, i) {
      var type = ds.type || config.type;
      if (PER_POINT[type]) {
        var n = (data.labels || ds.data || []).length;
        if (ds.backgroundColor === undefined) {
          ds.backgroundColor = Array.from({ length: n }, function (_, j) { return pal[j % pal.length]; });
        }
        if (ds.borderColor === undefined) ds.borderColor = token('--c-surface');
        return;
      }
      var c = pal[i % pal.length];
      if (ds.borderColor === undefined) ds.borderColor = c;
      if (ds.backgroundColor === undefined) {
        ds.backgroundColor = (type === 'line' || type === 'radar') ? alpha(c, 0.15) : alpha(c, 0.85);
      }
      if (type === 'line' && ds.tension === undefined) ds.tension = 0.3;
      if (type === 'bar' && ds.borderRadius === undefined) ds.borderRadius = 4;
      if ((type === 'line' || type === 'radar') && ds.pointBackgroundColor === undefined) ds.pointBackgroundColor = c;
    });
    if (config.type === 'radar') {
      var r = ((config.options = config.options || {}).scales = config.options.scales || {}).r = config.options.scales.r || {};
      r.angleLines = Object.assign({ color: token('--c-border') }, r.angleLines);
      r.grid = Object.assign({ color: token('--c-border') }, r.grid);
      r.pointLabels = Object.assign({ color: token('--c-text') }, r.pointLabels);
      r.ticks = Object.assign({ backdropColor: 'transparent', color: token('--c-text-muted') }, r.ticks);
    }
    return config;
  }

  function chart(el, config) {
    var target = null;
    try {
      target = typeof el === 'string' ? document.querySelector(el) : el;
      if (!target) throw new Error('Canvas.chart: no element for ' + el);
      if (!window.Chart) {
        problem(target, 'Chart.js is not loaded: add <script src="/ds/vendor/chart.umd.min.js"> to <head>.');
        return null;
      }
      var canvas = target;
      if (target.tagName !== 'CANVAS') {
        canvas = target.querySelector('canvas');
        if (!canvas) {
          canvas = document.createElement('canvas');
          target.appendChild(canvas);
        }
      }
      var type = config && config.type;
      if (!type || !window.Chart.registry.controllers.get(type)) {
        problem(target, 'Canvas.chart: unknown chart type "' + type + '"');
        return null;
      }
      var old = window.Chart.getChart(canvas);
      if (old) old.destroy();
      themeChartDefaults(window.Chart);
      return new window.Chart(canvas, colorize(config));
    } catch (e) {
      problem(target, 'Canvas.chart failed: ' + (e && e.message ? e.message : e));
      return null;
    }
  }

  function mermaidConfig() {
    var font = token('--font-sans');
    return {
      startOnLoad: false,
      securityLevel: 'strict',
      theme: 'base',
      fontFamily: font,
      themeVariables: {
        darkMode: isDark(),
        background: token('--c-surface'),
        fontFamily: font,
        fontSize: '14px',
        primaryColor: token('--c-accent-soft'),
        primaryTextColor: token('--c-text'),
        primaryBorderColor: token('--c-accent'),
        secondaryColor: token('--c-surface-2'),
        secondaryTextColor: token('--c-text'),
        secondaryBorderColor: token('--c-border'),
        tertiaryColor: token('--c-surface'),
        tertiaryTextColor: token('--c-text'),
        tertiaryBorderColor: token('--c-border'),
        lineColor: token('--c-text-muted'),
        textColor: token('--c-text'),
        mainBkg: token('--c-accent-soft'),
        nodeBorder: token('--c-accent'),
        clusterBkg: token('--c-surface-2'),
        clusterBorder: token('--c-border'),
        edgeLabelBackground: token('--c-surface'),
        noteBkgColor: token('--c-warn-soft'),
        noteTextColor: token('--c-text'),
        noteBorderColor: token('--c-warn'),
        actorBkg: token('--c-accent-soft'),
        actorBorder: token('--c-accent'),
        actorTextColor: token('--c-text'),
        signalColor: token('--c-text'),
        signalTextColor: token('--c-text')
      }
    };
  }

  function renderMermaid() {
    var blocks = Array.prototype.slice.call(document.querySelectorAll('pre.mermaid'));
    if (!blocks.length) return Promise.resolve();
    function fail(b, message, source) {
      if (source !== undefined) {
        /* drop mermaid's own error SVG and temp nodes; show the original source instead */
        b.textContent = source;
        Array.prototype.slice.call(document.querySelectorAll('body > [id^="dmermaid"], body > svg[id^="mermaid"]'))
          .forEach(function (n) { n.remove(); });
      }
      problem(b, message);
      b.setAttribute('data-processed', 'true');
    }
    if (!window.mermaid) {
      blocks.forEach(function (b) {
        fail(b, 'Mermaid is not loaded: add <script src="/ds/vendor/mermaid.min.js"> to <head>.');
      });
      return Promise.resolve();
    }
    try {
      window.mermaid.initialize(mermaidConfig());
    } catch (e) {
      blocks.forEach(function (b) {
        fail(b, 'Mermaid setup failed: ' + (e && e.message ? e.message : e));
      });
      return Promise.resolve();
    }
    return blocks.reduce(function (chain, b) {
      var source = b.textContent;
      return chain.then(function () {
        return Promise.resolve().then(function () {
          return window.mermaid.run({ nodes: [b] });
        }).catch(function (e) {
          fail(b, 'Diagram failed to render: ' + (e && e.message ? e.message : e), source);
        });
      });
    }, Promise.resolve());
  }

  var ready = new Promise(function (resolve) {
    function start() {
      var done = function () {
        document.documentElement.setAttribute('data-canvas-ready', '');
        resolve();
      };
      try {
        renderMermaid().then(done, done);
      } catch (e) {
        console.error('[canvas] ' + e);
        done();
      }
    }
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start);
    else start();
  });

  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', function () {
    window.location.reload();
  });

  window.Canvas = Object.freeze({ chart: chart, token: token, palette: palette, ready: ready });
})();
