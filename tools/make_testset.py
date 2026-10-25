"""Build a labeled test set from LFW: people with >=5 photos, 1500 one-off strangers,
300 synthetic group photos (2-3 people side by side) and 2 videos. Writes truth.json for evaluate.py.

Usage: python tools/make_testset.py <lfw dir> <out dir>
"""
import sys, random, shutil, cv2, json
from pathlib import Path
lfw, out = Path(sys.argv[1]), Path(sys.argv[2])
random.seed(1)
people = {d.name: sorted(d.glob('*.jpg')) for d in lfw.iterdir() if d.is_dir()}
multi = [n for n, f in people.items() if len(f) >= 5]
single = random.sample([n for n, f in people.items() if len(f) == 1], 1500)
truth = {}  # file -> list of names left-to-right
for n in multi + single:
    d = out / 'photos' / n[:1]; d.mkdir(parents=True, exist_ok=True)
    for f in people[n]:
        shutil.copy(f, d / f.name); truth[str(d / f.name)] = [n]
g = out / 'groups'; g.mkdir(parents=True, exist_ok=True)
for i in range(300):
    names = random.sample(multi, random.choice([2, 3]))
    imgs = [cv2.imread(str(random.choice(people[n]))) for n in names]
    p = g / f'group_{i}.jpg'; cv2.imwrite(str(p), cv2.hconcat(imgs)); truth[str(p)] = names
v = out / 'videos'; v.mkdir(parents=True, exist_ok=True)
for i, n in enumerate(['George_W_Bush', 'Serena_Williams']):
    p = v / f'clip_{i}.mp4'
    vw = cv2.VideoWriter(str(p), cv2.VideoWriter_fourcc(*'mp4v'), 5, (250, 250))
    for f in people[n][:20]:
        im = cv2.imread(str(f))
        for _ in range(5): vw.write(im)
    vw.release(); truth[str(p)] = [n]
json.dump(truth, open(out / 'truth.json', 'w'))
print(len(multi), 'people with >=5 photos;', len(truth), 'files')
