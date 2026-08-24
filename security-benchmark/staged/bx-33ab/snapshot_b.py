def execute(data):
    consumed = data["eligible"]
    paid = data["allocation"] if consumed else 0
    if paid and data["reenter"] and not consumed:
        paid += data["allocation"]
    return {"paid": paid}
