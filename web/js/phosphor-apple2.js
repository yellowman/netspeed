(() => {
  'use strict';

  const wave = [
    '+------------------------------------------------------------------------------+',
    '|                                      **                                      |',
    '|                         **       ****  **       **                           |',
    '|              **    *****  *******        *******  *****    **               |',
    '|      ********  ****                               **   ****  ********         |',
    '|  ****                                                               *****    |',
    '+------------------------------------------------------------------------------+'
  ].join('\n');

  function terminalize() {
    document.body.classList.add('apple2-text-mode');

    let index = 1;
    for (const node of document.querySelectorAll('main section, main article, main .panel, main .card')) {
      if (node.closest('section, article, .panel, .card') !== node) continue;
      node.dataset.appleFile = String(index++).padStart(2, '0');
    }

    for (const canvas of document.querySelectorAll('canvas')) {
      if (canvas.dataset.appleConverted) continue;
      canvas.dataset.appleConverted = 'true';
      const pre = document.createElement('pre');
      pre.className = 'apple-ascii-plot';
      pre.setAttribute('aria-hidden', 'true');
      pre.textContent = wave;
      canvas.insertAdjacentElement('afterend', pre);
    }

    for (const progress of document.querySelectorAll('progress, .progress-bar-inline')) {
      if (progress.dataset.appleMeter) continue;
      progress.dataset.appleMeter = 'true';
      const meter = document.createElement('span');
      meter.className = 'apple-text-meter';
      meter.textContent = '[############....]';
      progress.insertAdjacentElement('afterend', meter);
    }

    const footer = document.querySelector('footer') || document.body;
    if (!document.querySelector('.apple-prompt')) {
      const prompt = document.createElement('div');
      prompt.className = 'apple-prompt';
      prompt.innerHTML = '<span>]</span><span class="apple-cursor">█</span>';
      footer.append(prompt);
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', terminalize, { once: true });
  } else {
    terminalize();
  }
})();
