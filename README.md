# Activity Tracker

Something I can use to encourage me to not be so sedentary.

![Test Status](https://github.com/jamethy/activity-tracker/actions/workflows/go-build-and-test.yml/badge.svg)

This is all based on something I heard one time:

> You should do at least 30 minutes of moderate exercise three times a week

I wanted to track this for myself, but I was unhappy with what was out there:

- Required a fitbit or something similar
- Simple calendar would work, but constantly evaluating "the past seven days" seemed obnoxious
- I wanted to play with [templ](https://templ.guide/) and [htmx](https://htmx.org/)!

Starting with some business requirements:

- my maximum heart rate: 206.9 – (0.67 x age) = 184BPM
- my resting heart rate: 60ish
- [This](https://www.heart.org/en/healthy-living/fitness/fitness-basics/aha-recs-for-physical-activity-in-adults) says
    - get at least 2.5h/week of moderate-intensity aerobic activity (50-70% of maximum heart rate, 93-130BPM)
    - or 1.25h/w of vigorous aerobic activity (70-85% of maximum, 130-158)
    - **even light anything** is better than sitting
    - extra benefits after 5h

## Infrastructure
I can be extremely cheap, and I don't like DynamoDB, so what's the next easiest thing? Store it in a csv in S3!

![infrastructure.png](docs/infrastructure.png)

Insanity? Or genius?

